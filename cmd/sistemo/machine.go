package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func machineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "machine",
		Aliases: []string{"vm"},
		Short:   "Manage machines [alias: vm]",
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
	cmd.AddCommand(machineListCmd())
	cmd.AddCommand(machineDeployCmd())
	cmd.AddCommand(machineDeleteCmd())
	cmd.AddCommand(machineStopCmd())
	cmd.AddCommand(machineStartCmd())
	cmd.AddCommand(machineRestartCmd())
	cmd.AddCommand(machineTerminalCmd())
	cmd.AddCommand(machineStatusCmd())
	cmd.AddCommand(machineLogsCmd())
	cmd.AddCommand(machineExecCmd())
	cmd.AddCommand(machineSSHCmd())
	cmd.AddCommand(machineExposeCmd())
	cmd.AddCommand(machineUnexposeCmd())
	cmd.AddCommand(machineVolumeCmd())
	return cmd
}

func machineListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List machines",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runList(getLogger(cmd), getDBFromCmd(cmd))
		},
	}
}

func machineDeployCmd() *cobra.Command {
	var vcpus int
	var memoryStr, storageStr string
	var attach string
	var name string
	var expose []string
	var networkName string
	var rootVolume string
	cmd := &cobra.Command{
		Use:   "deploy [image] [flags]",
		Short: "Deploy a machine",
		Long: `Deploy a machine from an image name, file path, URL, or existing volume.

Sistemo resolves the image argument in this order:
  1. HTTP/HTTPS URL (downloaded by daemon)
  2. Local file path (if contains "/" or ends in ".ext4")
  3. Cached image in ~/.sistemo/images/
  4. Download from registry (registry.sistemo.io)

Use --volume to boot from an existing volume (no image needed):
  sistemo machine deploy --volume mydata --name restored

Override the default registry with SISTEMO_REGISTRY_URL.

Examples:
  sistemo machine deploy debian                              # auto-downloads if not cached
  sistemo machine deploy ./custom.rootfs.ext4                # local file
  sistemo machine deploy https://example.com/vm.ext4         # URL
  sistemo machine deploy debian --name dev --vcpus 4 --memory 2G
  sistemo machine deploy nginx --expose 80
  sistemo machine deploy --volume web-root --name web2       # boot from existing volume`,
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
	cmd.Flags().StringVar(&name, "name", "", "machine name (default: derived from image)")
	cmd.Flags().StringSliceVar(&expose, "expose", nil, "expose ports: hostPort:vmPort or just port (repeatable)")
	cmd.Flags().StringVar(&networkName, "network", "", "named network to join (default: shared sistemo0 bridge)")
	cmd.Flags().StringVar(&rootVolume, "volume", "", "boot from an existing volume (skip image cloning)")
	return cmd
}

func machineDeleteCmd() *cobra.Command {
	var preserveStorage bool
	var skipConfirm bool
	cmd := &cobra.Command{
		Use:               "delete <name|id>",
		Aliases:           []string{"rm", "remove", "destroy"},
		Short:             "Delete a machine (removes disk unless --preserve-storage)",
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
	cmd.Flags().BoolVar(&preserveStorage, "preserve-storage", false, "Keep the root volume after deleting the machine")
	cmd.Flags().BoolVarP(&skipConfirm, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

func machineStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name|id>",
		Short:             "Stop a machine (keeps disk)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStop(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func machineStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start <name|id>",
		Short:             "Start a stopped machine",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStart(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func machineRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "restart <name|id>",
		Short:             "Restart a machine (stop + start)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRestart(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func machineTerminalCmd() *cobra.Command {
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

func machineStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "status <name|id>",
		Aliases:           []string{"show", "info"},
		Short:             "Show machine details",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func machineLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "logs <name|id>",
		Short:             "Tail machine boot log",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(getLogger(cmd), getDBFromCmd(cmd), args[0])
		},
	}
}

func machineSSHCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ssh <name|id>",
		Short:             "SSH into a machine",
		Long:              "Open an interactive SSH session to a machine.\n\nExamples:\n  sistemo machine ssh myvm\n  sistemo machine ssh debian",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: vmNameCompletionFunc,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runSSH(getLogger(cmd), getDBFromCmd(cmd), getDataDirFromCmd(cmd), args[0])
		},
	}
}

func machineExecCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "exec <name|id> <command>",
		Short: "Run command in machine",
		Long:  "Run a command inside a machine via SSH.\n\nExamples:\n  sistemo machine exec myvm \"uname -a\"\n  sistemo machine exec myvm df -h\n  sistemo machine exec myvm -- ls -la /tmp",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runExec(getLogger(cmd), getDBFromCmd(cmd), args[0], strings.Join(args[1:], " "))
		},
	}
	cmd.Flags().SetInterspersed(false)
	return cmd
}
