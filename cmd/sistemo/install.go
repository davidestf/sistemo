package main

import (
	"github.com/spf13/cobra"
)

func installCmd() *cobra.Command {
	var upgrade bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Set up ~/.sistemo: download Firecracker + kernel, generate SSH key, check KVM",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runInstall(getLogger(cmd), getDataDirFromCmd(cmd), upgrade)
			return nil
		},
	}
	cmd.Flags().BoolVar(&upgrade, "upgrade", false, "Re-download Firecracker and kernel even if already present")
	return cmd
}
