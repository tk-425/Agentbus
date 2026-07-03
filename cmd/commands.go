package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tk-425/agentbus/internal/agenttypes"
	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
	"github.com/tk-425/agentbus/internal/registry"
	versionpkg "github.com/tk-425/agentbus/internal/version"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the broker in the background",
	RunE:  runStart,
}

// pidAlive reports whether a process with the given pid exists; swappable so
// tests can model a crashed broker without racing real pids.
var pidAlive = func(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// runningBroker returns the live broker recorded for projectRoot. A row whose
// pid is dead (crashed broker) is not live and must not block a new start —
// Serve's upsert repairs the stale row.
func runningBroker(d *sql.DB, projectRoot string) (pid, port int, live bool) {
	if err := d.QueryRow(
		`SELECT pid, port FROM brokers WHERE project_root = ?`, projectRoot,
	).Scan(&pid, &port); err != nil {
		return 0, 0, false
	}
	return pid, port, pidAlive(pid)
}

func runStart(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}

	// A second start must not launch a second broker: it would overwrite the
	// port file and hijack the brokers row, and either broker's shutdown
	// cleanup would then delete the survivor's registrations.
	if d, err := db.Open(sharedDBPath()); err == nil {
		pid, port, live := runningBroker(d, projectRoot)
		d.Close()
		if live {
			fmt.Printf("broker already running (pid %d, port %d)\n", pid, port)
			return nil
		}
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}

	logDir := filepath.Join(filepath.Dir(broker.DefaultPortFile()), "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(
		filepath.Join(logDir, filepath.Base(projectRoot)+".log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600,
	)
	if err != nil {
		return err
	}
	defer logFile.Close()

	bg := exec.Command(exe, "_broker")
	bg.Dir = projectRoot
	bg.Stdout = logFile
	bg.Stderr = logFile
	if err := bg.Start(); err != nil {
		return fmt.Errorf("launch broker: %w", err)
	}

	// The broker writes its port file once bound; wait briefly so the operator
	// sees the port (or a startup failure) instead of a silent return.
	portFile := broker.DefaultPortFile()
	for range 30 {
		if raw, err := os.ReadFile(portFile); err == nil {
			fmt.Printf("broker started (pid %d, port %s, log %s)\n",
				bg.Process.Pid, strings.TrimSpace(string(raw)), logFile.Name())
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("broker did not write %s within 3s — see %s", portFile, logFile.Name())
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the broker",
	RunE:  runStop,
}

func runStop(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}

	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()

	var pid int
	if err := d.QueryRow(
		`SELECT pid FROM brokers WHERE project_root = ?`, projectRoot,
	).Scan(&pid); err != nil {
		return fmt.Errorf("no running broker recorded for %s", projectRoot)
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	// SIGTERM triggers the _broker signal trap -> graceful Shutdown, which
	// removes the port file and brokers row on its way out.
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal broker (pid %d): %w", pid, err)
	}
	fmt.Printf("broker stopping (pid %d)\n", pid)
	return nil
}

var (
	registerName string
	registerPane string
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current tmux pane as an agent",
	RunE:  runRegister,
}

func runRegister(cmd *cobra.Command, args []string) error {
	paneID := resolvePaneID(registerPane)
	if paneID == "" {
		return fmt.Errorf("cannot determine pane: not inside tmux/herdr — pass --pane")
	}

	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}

	c, err := client.Dial(broker.DefaultPortFile())
	if err != nil {
		return fmt.Errorf("no running broker (run `agentbus start`): %w", err)
	}
	name, err := c.Register(filepath.Base(projectRoot), registerName, paneID)
	if err != nil {
		return err
	}
	fmt.Println(name)
	return nil
}

// resolvePaneID picks the pane the command runs in: an explicit --pane wins,
// then the multiplexer's own environment (TMUX_PANE / HERDR_PANE).
func resolvePaneID(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if pane := os.Getenv("TMUX_PANE"); pane != "" {
		return pane
	}
	return os.Getenv("HERDR_PANE")
}

var unregisterName string

var unregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Remove an agent from the registry",
	RunE:  runUnregister,
}

func runUnregister(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	localProject := filepath.Base(projectRoot)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		return err
	}

	r := registry.New()
	r.AttachDB(d, 0)
	inst, err := r.ResolveUnregisterTarget(localProject, unregisterName)
	if err != nil {
		return err
	}
	r.Unregister(inst.Project, inst.Name)
	fmt.Printf("removed %s@%s\n", inst.Name, inst.Project)
	return nil
}

var (
	sendTo   string
	sendFrom string
)

var sendCmd = &cobra.Command{
	Use:   "send --from <agent> --to <agent> <message>",
	Short: "Send a message to an agent",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSend,
}

func runSend(cmd *cobra.Command, args []string) error {
	c, err := client.Dial(broker.DefaultPortFile())
	if err != nil {
		return fmt.Errorf("no running broker (run `agentbus start`): %w", err)
	}
	// The broker routes the Request (locally or cross-broker) and errors loudly
	// on an unknown Agent instance.
	return c.Send(message.Message{
		ID:        message.NewID(),
		Kind:      message.KindRequest,
		From:      sendFrom,
		To:        sendTo,
		Body:      strings.Join(args, " "),
		CreatedAt: time.Now().UTC(),
	})
}

var (
	inboxName    string
	inboxWait    bool
	inboxTimeout time.Duration
)

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Read pending messages (marks as read)",
	RunE:  runInbox,
}

func runInbox(cmd *cobra.Command, args []string) error {
	c, err := client.Dial(broker.DefaultPortFile())
	if err != nil {
		return fmt.Errorf("no running broker (run `agentbus start`): %w", err)
	}

	deadline := time.Now().Add(inboxTimeout)
	for {
		msgs := c.Inbox(inboxName)
		for _, m := range msgs {
			fmt.Printf("[%s] from %s: %s\n", m.Kind, m.From, m.Body)
		}
		if len(msgs) > 0 || !inboxWait || time.Now().After(deadline) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
}

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered agents",
	RunE:  runList,
}

func runList(cmd *cobra.Command, args []string) error {
	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		return err
	}

	r := registry.New()
	r.AttachDB(d, 0)
	agents, err := r.ListShared()
	if err != nil {
		return err
	}
	for _, inst := range agents {
		fmt.Printf("%s@%s\n", inst.Name, inst.Project)
	}
	return nil
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show statusline data",
	RunE:  runStatus,
}

func runStatus(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	localProject := filepath.Base(projectRoot)

	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		return err
	}

	var brokerCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM brokers WHERE project_root = ?`, projectRoot).Scan(&brokerCount); err != nil {
		return err
	}
	var agentCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM agents WHERE project = ?`, localProject).Scan(&agentCount); err != nil {
		return err
	}
	var historyCount int
	if err := d.QueryRow(`SELECT COUNT(*) FROM messages`).Scan(&historyCount); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(cmd.OutOrStdout(), statusLine(localProject, brokerCount > 0, agentCount, historyCount, versionpkg.String)); err != nil {
		return err
	}
	return nil
}

func statusLine(project string, brokerRunning bool, agentCount, historyCount int, version string) string {
	brokerState := "down"
	if brokerRunning {
		brokerState = "up"
	}
	return fmt.Sprintf("project=%s broker=%s agents=%d history=%d version=%s", project, brokerState, agentCount, historyCount, version)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the agentbus version",
	RunE:  runVersion,
}

func runVersion(cmd *cobra.Command, args []string) error {
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), versionpkg.String); err != nil {
		return err
	}
	return nil
}

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show recent message history",
	RunE:  runLog,
}

func runLog(cmd *cobra.Command, args []string) error {
	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		return err
	}

	history, err := db.RecentMessages(d, 20)
	if err != nil {
		return err
	}
	for _, msg := range history {
		fmt.Printf("[%s] %s -> %s: %s\n", msg.Kind, msg.From, msg.To, msg.Body)
	}
	return nil
}

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Scan tmux panes and auto-register agents in project directory",
	RunE:  runDiscover,
}

var (
	addAgentName          string
	addAgentPromptPattern string
	addAgentResponseWait  int
)

var addAgentCmd = &cobra.Command{
	Use:   "add-agent",
	Short: "Add a custom agent type to agents.json",
	RunE:  runAddAgent,
}

func runAddAgent(cmd *cobra.Command, args []string) error {
	store := agenttypes.New(agentTypesPath())
	if err := store.Add(addAgentName, agenttypes.Definition{
		PromptPattern: addAgentPromptPattern,
		ResponseWait:  addAgentResponseWait,
	}); err != nil {
		return err
	}
	fmt.Printf("added %s\n", strings.ToLower(strings.TrimSpace(addAgentName)))
	return nil
}

func runDiscover(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
	}
	localProject := filepath.Base(projectRoot)

	defs, err := loadAgentDefinitions()
	if err != nil {
		return err
	}

	d, err := db.Open(sharedDBPath())
	if err != nil {
		return err
	}
	defer d.Close()
	if err := db.Migrate(d); err != nil {
		return err
	}
	r := registry.New()
	r.AttachDB(d, 0)
	shared, err := r.ListShared()
	if err != nil {
		return err
	}
	bound := make(map[string]bool, len(shared))
	for _, inst := range shared {
		if inst.Project == localProject && inst.PaneID != "" {
			bound[inst.PaneID] = true
		}
	}

	c, err := client.Dial(broker.DefaultPortFile())
	if err != nil {
		return fmt.Errorf("no running broker (run `agentbus start`): %w", err)
	}
	created, err := discoverWith(projectRoot, multiplexer.Detect(), defs, bound, func(agentType, paneID string) (string, error) {
		return c.Register(localProject, agentType, paneID)
	})
	if err != nil {
		return err
	}
	for _, name := range created {
		fmt.Println(name)
	}
	return nil
}

type discoverCandidate struct {
	PaneID    string
	AgentType string
}

func discoverWith(projectRoot string, mux multiplexer.Multiplexer, defs map[string]agenttypes.Definition, bound map[string]bool, register func(agentType, paneID string) (string, error)) ([]string, error) {
	panes, err := mux.ListPanes()
	if err != nil {
		return nil, err
	}
	candidates := discoverCandidates(projectRoot, panes, defs, bound)
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		name, err := register(candidate.AgentType, candidate.PaneID)
		if err != nil {
			return nil, err
		}
		bound[candidate.PaneID] = true
		out = append(out, name)
	}
	return out, nil
}

func discoverCandidates(projectRoot string, panes []multiplexer.Pane, defs map[string]agenttypes.Definition, bound map[string]bool) []discoverCandidate {
	out := make([]discoverCandidate, 0, len(panes))
	for _, pane := range panes {
		if bound[pane.ID] || !paneInProject(projectRoot, pane.CWD) {
			continue
		}
		agentType := normalizeCommandBasename(pane.Command)
		if _, ok := defs[agentType]; !ok {
			continue
		}
		out = append(out, discoverCandidate{PaneID: pane.ID, AgentType: agentType})
	}
	return out
}

func paneInProject(projectRoot, cwd string) bool {
	if cwd == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(projectRoot), filepath.Clean(cwd))
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

func normalizeCommandBasename(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	parts := strings.Fields(command)
	return strings.ToLower(filepath.Base(parts[0]))
}

func agentTypesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "agents.json")
	}
	return filepath.Join(home, ".agentbus", "agents.json")
}

func loadAgentDefinitions() (map[string]agenttypes.Definition, error) {
	return agenttypes.New(agentTypesPath()).Load()
}

func init() {
	registerCmd.Flags().StringVar(&registerName, "name", "", "agent type to register (e.g. claude)")
	if err := registerCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	}
	registerCmd.Flags().StringVar(&registerPane, "pane", "", "pane ID (defaults to the current multiplexer pane)")

	sendCmd.Flags().StringVar(&sendTo, "to", "", "target Agent instance (name or name@project)")
	if err := sendCmd.MarkFlagRequired("to"); err != nil {
		panic(err)
	}
	sendCmd.Flags().StringVar(&sendFrom, "from", "", "sending Agent instance (Replies route back to it)")
	if err := sendCmd.MarkFlagRequired("from"); err != nil {
		panic(err)
	}

	inboxCmd.Flags().StringVar(&inboxName, "name", "", "Agent instance whose inbox to read")
	if err := inboxCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	}
	inboxCmd.Flags().BoolVar(&inboxWait, "wait", false, "block until a message arrives")
	inboxCmd.Flags().DurationVar(&inboxTimeout, "timeout", 30*time.Second, "max time --wait blocks")

	unregisterCmd.Flags().StringVar(&unregisterName, "name", "", "target Agent instance to remove (name or name@project)")
	if err := unregisterCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	}

	addAgentCmd.Flags().StringVarP(&addAgentName, "name", "n", "", "custom agent type name")
	if err := addAgentCmd.MarkFlagRequired("name"); err != nil {
		panic(err)
	}
	addAgentCmd.Flags().StringVarP(&addAgentPromptPattern, "prompt-pattern", "p", "", "optional prompt pattern regex")
	addAgentCmd.Flags().IntVarP(&addAgentResponseWait, "response-wait", "w", 2, "response wait in seconds")
}
