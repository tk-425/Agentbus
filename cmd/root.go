package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/tk-425/agentbus/internal/broker"
	"github.com/tk-425/agentbus/internal/client"
	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/multiplexer"
	"github.com/tk-425/agentbus/internal/registry"
	versionpkg "github.com/tk-425/agentbus/internal/version"
	"github.com/tk-425/agentbus/internal/watcher"
)

var rootCmd = &cobra.Command{
	Use:     "agentbus",
	Short:   "Local multi-agent message bus for AI coding agents",
	Version: versionpkg.String,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// brokerCmd runs the broker in the foreground, blocking until SIGTERM/SIGINT.
// It is hidden: `agentbus start` launches it in the background, and `agentbus
// stop` signals it, letting Serve's deferred cleanup remove the port file and
// brokers row.
var brokerCmd = &cobra.Command{
	Use:    "_broker",
	Short:  "Run the broker in the foreground (used by start)",
	Hidden: true,
	RunE:   runBroker,
}

func runBroker(cmd *cobra.Command, args []string) error {
	projectRoot, err := os.Getwd()
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

	b := broker.New()
	b.AttachDB(d)
	// The project label agents are addressed by is the project-root basename
	// (finding F1).
	b.Registry.SetLocalProject(filepath.Base(projectRoot))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		b.Shutdown(shutdownCtx)
	}()

	mux, err := multiplexer.Detect()
	if err != nil {
		return err
	}
	// Run per-agent Watcher loops inside the broker process: each injects queued
	// Requests into its registered pane and returns Replies. Watchers live and
	// die with the broker (they respawn on restart), so there is no separate
	// reconnect path. The loop uses an in-process Client — no import cycle, since
	// the watcher/client packages cannot depend on cmd.
	go superviseWatchers(ctx, b, mux)
	go autoDiscoverLoop(ctx, projectRoot, b, mux)

	return b.Serve(projectRoot, broker.DefaultPortFile(), mux)
}

func autoDiscoverLoop(ctx context.Context, projectRoot string, b *broker.Broker, mux multiplexer.Multiplexer) {
	ticker := time.NewTicker(watcherPoll)
	defer ticker.Stop()
	for {
		if err := autoDiscoverOnceWithBackends(projectRoot, b, multiplexer.Backends()); err != nil {
			fmt.Fprintf(os.Stderr, "auto-discover: %v\n", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func autoDiscoverOnce(projectRoot string, b *broker.Broker, mux multiplexer.Multiplexer) error {
	backend := multiplexer.NameOf(mux)
	return autoDiscoverOnceWithBackends(projectRoot, b, []multiplexer.Backend{{Name: backend, Multiplexer: mux}})
}

func autoDiscoverOnceWithBackends(projectRoot string, b *broker.Broker, backends []multiplexer.Backend) error {
	defs, err := loadAgentDefinitions()
	if err != nil {
		return err
	}
	localProject := filepath.Base(projectRoot)
	candidates := discoverCandidatesFromBackends(projectRoot, backends, defs, map[string]bool{})
	candidateByKey := make(map[string]discoverCandidate, len(candidates))
	candidateByPane := make(map[string]discoverCandidate, len(candidates))
	for _, candidate := range candidates {
		candidateByKey[discoveryKey(candidate.Backend, candidate.PaneID)] = candidate
		candidateByPane[candidate.PaneID] = candidate
	}

	for _, inst := range b.Registry.All() {
		if inst.Project != localProject || inst.PaneID == "" {
			continue
		}
		candidate, ok := candidateByKey[discoveryKey(inst.Backend, inst.PaneID)]
		if inst.Backend == "" {
			candidate, ok = candidateByPane[inst.PaneID]
		}
		if !ok || inst.Backend != candidate.Backend || !strings.HasPrefix(inst.Name, candidate.AgentType+"-") {
			b.Registry.Unregister(inst.Project, inst.Name)
		}
	}

	bound := map[string]bool{}
	for _, inst := range b.Registry.All() {
		if inst.Project == localProject && inst.PaneID != "" {
			if inst.Backend == "" {
				bound[inst.PaneID] = true
			} else {
				bound[discoveryKey(inst.Backend, inst.PaneID)] = true
			}
		}
	}
	_, err = discoverWithBackends(projectRoot, backends, defs, bound, func(agentType, paneID, backend string) (string, error) {
		return b.Register(localProject, agentType, paneID, backend)
	})
	return err
}

// watcherPoll bounds how often the supervisor scans for newly registered Agent
// instances and how often each Watcher rechecks its inbox.
const watcherPoll = 500 * time.Millisecond

// superviseWatchers starts one Watcher goroutine per registered Agent instance
// and keeps watching for new registrations until ctx is cancelled. Each instance
// is watched at most once.
func superviseWatchers(ctx context.Context, b *broker.Broker, mux multiplexer.Multiplexer) {
	c := client.New(b)
	watched := map[string]bool{}
	ticker := time.NewTicker(watcherPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, inst := range b.Registry.All() {
				if inst.PaneID == "" || watched[inst.Name] {
					continue
				}
				watched[inst.Name] = true
				instanceMux := backendForInstance(inst, mux)
				go watchLoop(ctx, inst.Name, inst.PaneID, instanceMux, c)
			}
		}
	}
}

func backendForInstance(inst registry.Instance, fallback multiplexer.Multiplexer) multiplexer.Multiplexer {
	if resolved, ok := multiplexer.For(inst.Backend); ok {
		return resolved
	}
	return fallback
}

// watchLoop delivers Requests for one Agent instance until ctx is cancelled,
// running a Watcher pass every watcherPoll.
func watchLoop(ctx context.Context, agent, paneID string, mux multiplexer.Multiplexer, c *client.Client) {
	ticker := time.NewTicker(watcherPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := watcher.Watch(agent, paneID, mux, c); err != nil {
				fmt.Fprintf(os.Stderr, "watcher %s: %v\n", agent, err)
			}
		}
	}
}

// sharedDBPath returns ~/.agentbus/agentbus.db, the shared registry and
// message store all brokers use.
func sharedDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "agentbus.db")
	}
	return filepath.Join(home, ".agentbus", "agentbus.db")
}

func init() {
	rootCmd.AddCommand(
		brokerCmd,
		startCmd,
		stopCmd,
		registerCmd,
		unregisterCmd,
		whoamiCmd,
		sendCmd,
		replyCmd,
		inboxCmd,
		listCmd,
		statusCmd,
		versionCmd,
		logCmd,
		discoverCmd,
		addAgentCmd,
	)
}
