package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "agentbus",
	Short: "Local multi-agent message bus for AI coding agents",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(
		startCmd,
		stopCmd,
		registerCmd,
		unregisterCmd,
		sendCmd,
		inboxCmd,
		listCmd,
		statusCmd,
		logCmd,
		discoverCmd,
		addAgentCmd,
	)
}
