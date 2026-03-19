package main

import "github.com/spf13/cobra"

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage images (list, pull, build)",
	}
	cmd.AddCommand(imageListCmd())
	cmd.AddCommand(imagePullCmd())
	cmd.AddCommand(imageBuildCmd())
	return cmd
}

func imageListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List local and available remote images",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runImageList(getDataDirFromCmd(cmd))
			return nil
		},
	}
}

func imagePullCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pull <name>",
		Short: "Download an image from the registry",
		Long:  "Downloads an image from the Sistemo registry to ~/.sistemo/images/. Override the registry with SISTEMO_REGISTRY_URL.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runImagePull(getLogger(cmd), getDataDirFromCmd(cmd), args[0])
		},
	}
}

func imageBuildCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "build <docker-image> [output-path]",
		Short: "Build rootfs from Docker image (requires Docker and root)",
		Long: `Converts a Docker image to a Firecracker rootfs (ext4) and saves it to ~/.sistemo/images/.
The rootfs includes the daemon's SSH public key so terminal and exec work.

Examples:
  sudo sistemo image build debian
  sudo sistemo image build myapp:latest
  sudo sistemo image build myapp /custom/path/output.rootfs.ext4`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			image := args[0]
			outPath := ""
			if len(args) >= 2 {
				outPath = args[1]
			}
			return runBuild(getLogger(cmd), getDataDirFromCmd(cmd), image, outPath)
		},
	}
}
