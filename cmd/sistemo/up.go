package main

import (
	"github.com/spf13/cobra"
)

func upCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Start the daemon (HTTP API)",
		Long:  "Start the Sistemo daemon. Must run as root for VM networking (e.g. sudo sistemo up).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDaemon(getLogger(cmd), getDataDirFromCmd(cmd))
		},
	}
}
