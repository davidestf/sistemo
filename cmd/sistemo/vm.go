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
		Short: "Manage VMs (list, deploy, start, stop, delete, terminal, status, logs, exec)",
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
	cmd.AddCommand(vmDeleteCmd())
	cmd.AddCommand(vmStopCmd())
	cmd.AddCommand(vmStartCmd())
	cmd.AddCommand(vmRestartCmd())
	cmd.AddCommand(vmTerminalCmd())
	cmd.AddCommand(vmStatusCmd())
	cmd.AddCommand(vmLogsCmd())
	cmd.AddCommand(vmExecCmd())
	cmd.AddCommand(vmSSHCmd())
	cmd.AddCommand(vmExposeCmd())
	cmd.AddCommand(vmUnexposeCmd())
	cmd.AddCommand(vmVolumeCmd())
	return cmd
}

func vmListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List VMs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(getLogger(cmd), getDBFromCmd(cmd))
		},
	}
}

func vmDeployCmd() *cobra.Command {
	var vcpus int
	var memoryStr, storageStr string
	var attach string
	var name string
	var expose []string
	var networkName string
	var rootVolume string
	cmd := &cobra.Command{
		Use:   "deploy [image] [flags]",
		Short: "Deploy a VM",
		Long: `Deploy a VM from an image name, file path, URL, or existing volume.

Sistemo resolves the image argument in this order:
  1. HTTP/HTTPS URL (downloaded by daemon)
  2. Local file path (if contains "/" or ends in ".ext4")
  3. Cached image in ~/.sistemo/images/
  4. Download from registry (registry.sistemo.io)

Use --volume to boot from an existing volume (no image needed):
  sistemo vm deploy --volume mydata --name restored

Override the default registry with SISTEMO_REGISTRY_URL.

Examples:
  sistemo vm deploy debian                              # auto-downloads if not cached
  sistemo vm deploy ./custom.rootfs.ext4                # local file
  sistemo vm deploy https://example.com/vm.ext4         # URL
  sistemo vm deploy debian --name dev --vcpus 4 --memory 2G
  sistemo vm deploy nginx --expose 80
  sistemo vm deploy --volume web-root --name web2       # boot from existing volume`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			dataDir := getDataDirFromCmd(cmd)

			var imageArg string
			if len(args) > 0 {
				imageArg = args[0]
			}

			if imageArg == "" && rootVolume == "" {
				return fmt.Errorf("provide an image name or --volume to boot from an existing volume")
			}

			if vcpus < 1 || vcpus > 64 {
				return fmt.Errorf("invalid --vcpus %d: must be between 1 and 64", vcpus)
			}

			memoryMB, err := parseSizeMB(memoryStr)
			if err != nil {
				return fmt.Errorf("invalid --memory %q: %w (use a number in MB, or e.g. 1G, 2GB)", memoryStr, err)
			}
			if memoryMB < 128 {
				return fmt.Errorf("invalid --memory: minimum is 128 MB")
			}

			storageMB, err := parseSizeMB(storageStr)
			if err != nil {
				return fmt.Errorf("invalid --storage %q: %w (use a number in MB, or e.g. 1G, 10GB)", storageStr, err)
			}

			if imageArg != "" {
				imageArg, err = resolveImage(logger, dataDir, imageArg)
				if err != nil {
					return err
				}
			}

			var attachVolumes []string
			if attach != "" {
				for _, idOrName := range strings.Split(attach, ",") {
					idOrName = strings.TrimSpace(idOrName)
					if idOrName == "" {
						continue
					}
					attachVolumes = append(attachVolumes, idOrName)
				}
			}
			return runDeploy(getLogger(cmd), getDBFromCmd(cmd), imageArg, vcpus, memoryMB, storageMB, attachVolumes, name, expose, networkName, rootVolume)
		},
	}
	cmd.Flags().IntVar(&vcpus, "vcpus", 2, "number of vCPUs")
	cmd.Flags().StringVar(&memoryStr, "memory", "512", "memory: number (MB) or e.g. 2G, 2GB")
	cmd.Flags().StringVar(&storageStr, "storage", "2048", "storage: number (MB) or e.g. 10G, 10GB")
	cmd.Flags().StringVar(&attach, "attach", "", "comma-separated volume IDs or names to attach as extra disks")
	cmd.Flags().StringVar(&name, "name", "", "VM name (default: derived from image)")
	cmd.Flags().StringSliceVar(&expose, "expose", nil, "expose ports: hostPort:vmPort or just port (repeatable)")
	cmd.Flags().StringVar(&networkName, "network", "", "named network to join (default: shared sistemo0 bridge)")
	cmd.Flags().StringVar(&rootVolume, "volume", "", "boot from an existing volume (skip image cloning)")
	return cmd
}

func vmDeleteCmd() *cobra.Command {
	var preserveStorage bool
	var skipConfirm bool
	cmd := &cobra.Command{
		Use:               "delete <name|id>",
		Aliases:           []string{"rm", "remove", "destroy"},
		Short:             "Delete a VM (removes disk unless --preserve-storage)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !skipConfirm && !confirmAction("delete", args[0]) {
				fmt.Println("Cancelled.")
				return nil
			}
			return runDelete(getLogger(cmd), getDBFromCmd(cmd), args[0], preserveStorage)
		},
	}
	cmd.Flags().BoolVar(&preserveStorage, "preserve-storage", false, "Keep the root volume after deleting the VM")
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func vmStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name|id>",
		Short:             "Stop a VM (keeps disk)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start <name|id>",
		Short:             "Start a stopped VM",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "restart <name|id>",
		Short:             "Restart a VM (stop + start)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestart(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmTerminalCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "terminal <name|id>",
		Short:             "Open terminal in browser",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTerminal(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status <name|id>",
		Aliases:           []string{"show", "info"},
		Short:             "Show VM details",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "logs <name|id>",
		Short:             "Tail VM boot log",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func vmSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ssh <name|id>",
		Short:             "SSH into a VM",
		Long:              "Open an interactive SSH session to a VM.\n\nExamples:\n  sistemo vm ssh myvm\n  sistemo vm ssh debian",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSH(getLogger(cmd), getDBFromCmd(cmd), getDataDirFromCmd(cmd), args[0])
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
			return runExec(getLogger(cmd), getDBFromCmd(cmd), args[0], strings.Join(args[1:], " "))
		},
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}
