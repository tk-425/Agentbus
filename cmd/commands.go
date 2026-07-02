package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the broker in the background",
	RunE:  runStart,
}

func runStart(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
	if err != nil {
		return err
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

var unregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Remove an agent from the registry",
	Run:   notImplemented,
}

var (
	sendTo   string
	sendFrom string
)

var sendCmd = &cobra.Command{
	Use:   "send --to <agent> <message>",
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
	Run:   notImplemented,
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show statusline data",
	Run:   notImplemented,
}

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "Show recent message history",
	Run:   notImplemented,
}

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "Scan tmux panes and auto-register agents in project directory",
	Run:   notImplemented,
}

var addAgentCmd = &cobra.Command{
	Use:   "add-agent",
	Short: "Add a custom agent type to agents.json",
	Run:   notImplemented,
}

func notImplemented(cmd *cobra.Command, args []string) {
	fmt.Printf("%s: not yet implemented\n", cmd.Use)
}

func init() {
	registerCmd.Flags().StringVar(&registerName, "name", "", "agent type to register (e.g. claude)")
	registerCmd.MarkFlagRequired("name")
	registerCmd.Flags().StringVar(&registerPane, "pane", "", "pane ID (defaults to the current multiplexer pane)")

	sendCmd.Flags().StringVar(&sendTo, "to", "", "target Agent instance (name or name@project)")
	sendCmd.MarkFlagRequired("to")
	sendCmd.Flags().StringVar(&sendFrom, "from", "", "sending Agent instance (Replies route back to it)")

	inboxCmd.Flags().StringVar(&inboxName, "name", "", "Agent instance whose inbox to read")
	inboxCmd.MarkFlagRequired("name")
	inboxCmd.Flags().BoolVar(&inboxWait, "wait", false, "block until a message arrives")
	inboxCmd.Flags().DurationVar(&inboxTimeout, "timeout", 30*time.Second, "max time --wait blocks")
}
