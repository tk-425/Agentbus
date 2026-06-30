package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var startCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the broker in the background",
	Run:   notImplemented,
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the broker",
	Run:   notImplemented,
}

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register the current tmux pane as an agent",
	Run:   notImplemented,
}

var unregisterCmd = &cobra.Command{
	Use:   "unregister",
	Short: "Remove an agent from the registry",
	Run:   notImplemented,
}

var sendCmd = &cobra.Command{
	Use:   "send",
	Short: "Send a message to an agent",
	Run:   notImplemented,
}

var inboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Read pending messages (marks as read)",
	Run:   notImplemented,
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
