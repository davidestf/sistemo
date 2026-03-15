package main

import (
	"github.com/spf13/cobra"
)

func sshKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ssh-key",
		Short: "Print SSH public key to add to VM images (for terminal)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runSshKey(getLogger(cmd), getDataDirFromCmd(cmd))
			return nil
		},
	}
}
