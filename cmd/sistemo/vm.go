package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func vmCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "vm",
		Short: "Manage VMs (list, deploy, start, stop, destroy, terminal, status, logs, exec)",
	}
	cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		// Root's PersistentPreRunE already ran; get dataDir from root's context (child may not inherit context yet)
		root := cmd.Root()
		dataDir, _ := root.Context().Value(contextKeyDataDir).(string)
		if dataDir == "" {
			dataDir = getDataDir(root.PersistentFlags().Lookup("data-dir").Value.String())
		}
		database, err := getDB(dataDir)
		if err != nil {
			return err
		}
		ctx := root.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		ctx = context.WithValue(ctx, contextKeyDB, database)
		cmd.SetContext(ctx)
		return nil
	}
	cmd.PersistentPostRunE = func(cmd *cobra.Command, _ []string) error {
		if db := getDBFromCmd(cmd); db != nil {
			_ = db.Close()
		}
		return nil
	}
	cmd.AddCommand(vmListCmd())
	cmd.AddCommand(vmDeployCmd())
	cmd.AddCommand(vmDestroyCmd())
	cmd.AddCommand(vmStopCmd())
	cmd.AddCommand(vmStartCmd())
	cmd.AddCommand(vmTerminalCmd())
	cmd.AddCommand(vmStatusCmd())
	cmd.AddCommand(vmLogsCmd())
	cmd.AddCommand(vmExecCmd())
	return cmd
}

func vmListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List VMs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runList(getLogger(cmd), getDBFromCmd(cmd))
			return nil
		},
	}
}

func vmDeployCmd() *cobra.Command {
	var vcpus int
	var memoryStr, storageStr string
	var attach string
	var name string
	cmd := &cobra.Command{
		Use:   "deploy <image> [flags]",
		Short: "Deploy a VM",
		Long: `Deploy a VM from an image name, file path, or URL.

Sistemo resolves the image argument in this order:
  1. HTTP/HTTPS URL (downloaded by daemon)
  2. Local file path (if contains "/" or ends in ".ext4")
  3. Cached image in ~/.sistemo/images/
  4. Download from registry (registry.sistemo.io)

Override the default registry with SISTEMO_REGISTRY_URL.

Examples:
  sistemo vm deploy debian                          # auto-downloads if not cached
  sistemo vm deploy ./custom.rootfs.ext4            # local file
  sistemo vm deploy https://example.com/vm.ext4     # URL
  sistemo vm deploy debian --name dev --vcpus 4 --memory 2G`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			dataDir := getDataDirFromCmd(cmd)
			imageArg := args[0]
			memoryMB, err := parseSizeMB(memoryStr)
			if err != nil {
				return err
			}
			storageMB, err := parseSizeMB(storageStr)
			if err != nil {
				return err
			}

			imageArg = resolveImage(logger, dataDir, imageArg)

			var attachPaths []string
			if attach != "" {
				for _, idOrName := range strings.Split(attach, ",") {
					idOrName = strings.TrimSpace(idOrName)
					if idOrName == "" {
						continue
					}
					p := resolveVolumePath(dataDir, idOrName)
					if p == "" {
						return fmt.Errorf("volume not found for attach: %q", idOrName)
					}
					attachPaths = append(attachPaths, p)
				}
			}
			runDeploy(getLogger(cmd), getDBFromCmd(cmd), imageArg, vcpus, memoryMB, storageMB, attachPaths, name)
			return nil
		},
	}
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "number of vCPUs")
	cmd.Flags().StringVar(&memoryStr, "memory", "512", "memory: number (MB) or e.g. 2G, 2GB")
	cmd.Flags().StringVar(&storageStr, "storage", "2048", "storage: number (MB) or e.g. 10G, 10GB")
	cmd.Flags().StringVar(&attach, "attach", "", "comma-separated volume IDs or names to attach as extra disks")
	cmd.Flags().StringVar(&name, "name", "", "VM name (default: derived from image)")
	return cmd
}

func vmDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <name|id>",
		Short: "Destroy a VM (removes disk)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runDestroy(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name|id>",
		Short: "Stop a VM (keeps disk)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runStop(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name|id>",
		Short: "Start a stopped VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runStart(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmTerminalCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "terminal <name|id>",
		Short: "Open terminal in browser",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runTerminal(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status <name|id>",
		Short: "Show VM details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runStatus(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs <name|id>",
		Short: "Tail VM boot log",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			runLogs(getLogger(cmd), getDBFromCmd(cmd), args[0])
			return nil
		},
	}
}

func vmExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name|id> <command>",
		Short: "Run command in VM",
		Long:  "Run a command inside a VM via SSH.\n\nExamples:\n  sistemo vm exec myvm \"uname -a\"\n  sistemo vm exec myvm df -h\n  sistemo vm exec myvm -- ls -la /tmp",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			runExec(getLogger(cmd), getDBFromCmd(cmd), args[0], strings.Join(args[1:], " "))
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}
