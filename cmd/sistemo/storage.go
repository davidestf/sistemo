package main

import (
	"fmt"

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
	cmd.AddCommand(volumeResizeCmd())
	cmd.AddCommand(volumeShowCmd())
	return cmd
}

// vmVolumeCmd returns the "vm volume" subcommand group for attach/detach.
func vmVolumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "volume",
		Short: "Attach or detach volumes from a VM",
	}
	cmd.AddCommand(volumeAttachCmd())
	cmd.AddCommand(volumeDetachCmd())
	return cmd
}

func volumeCreateCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:     "create <size>",
		Aliases: []string{"add"},
		Short:   "Create a volume",
		Long:  "Create a persistent volume. Size can be a number (MB) or e.g. 1G, 5GB, 512MB.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sizeMB, err := parseSizeMB(args[0])
			if err != nil || sizeMB < 1 {
				cmd.SilenceUsage = true
				return fmt.Errorf("volume create: invalid size %q (use a number in MB, or e.g. 1G, 5GB)", args[0])
			}
			return runStorageCreate(getLogger(cmd), sizeMB, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Volume name (default: first 8 chars of ID)")
	return cmd
}

func volumeListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List volumes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runStorageList(getLogger(cmd))
		},
	}
}

func volumeDeleteCmd() *cobra.Command {
	var skipConfirm bool
	cmd := &cobra.Command{
		Use:     "delete <volume-id|name>",
		Aliases: []string{"rm", "remove"},
		Short:   "Delete a volume",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !skipConfirm && !confirmAction("delete", args[0]) {
				fmt.Println("Cancelled.")
				return nil
			}
			return runStorageDelete(getLogger(cmd), args[0])
		},
	}
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func volumeShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "show <volume-id|name>",
		Aliases: []string{"info", "get"},
		Short:   "Show volume details",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageShow(getLogger(cmd), args[0])
		},
	}
}

func volumeResizeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resize <volume-id|name> <new-size>",
		Short: "Resize a volume (grow only, VM must be stopped)",
		Long:  "Grow a volume to a new size. Size can be a number (MB) or e.g. 5G, 10GB. Shrinking is not supported.",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sizeMB, err := parseSizeMB(args[1])
			if err != nil || sizeMB < 1 {
				cmd.SilenceUsage = true
				return fmt.Errorf("volume resize: invalid size %q (use a number in MB, or e.g. 5G, 10GB)", args[1])
			}
			return runStorageResize(getLogger(cmd), args[0], sizeMB)
		},
	}
}

func volumeAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <vm-name|id> <volume-name|id>",
		Short: "Attach a volume to a stopped VM",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageAttach(getLogger(cmd), args[0], args[1])
		},
	}
}

func volumeDetachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "detach <vm-name|id> <volume-name|id>",
		Short: "Detach a volume from a stopped VM",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageDetach(getLogger(cmd), args[0], args[1])
		},
	}
}
