package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"
)

func volumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage persistent volumes (attach at deploy with --attach)",
	}
	cmd.AddCommand(volumeCreateCmd())
	cmd.AddCommand(volumeListCmd())
	cmd.AddCommand(volumeDeleteCmd())
	return cmd
}

func volumeCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "create <size_mb>",
		Short: "Create a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sizeMB, err := strconv.Atoi(args[0])
			if err != nil || sizeMB < 1 {
				cmd.SilenceUsage = true
				return fmt.Errorf("volume create: size_mb must be a positive integer")
			}
			return runStorageCreate(getLogger(cmd), getDataDirFromCmd(cmd), sizeMB, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Volume name (default: first 8 chars of ID)")
	return cmd
}

func volumeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List volumes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStorageList(getLogger(cmd), getDataDirFromCmd(cmd))
		},
	}
}

func volumeDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <volume-id|name>",
		Short: "Delete a volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageDelete(getLogger(cmd), getDataDirFromCmd(cmd), args[0])
		},
	}
}
