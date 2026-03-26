package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
)

const unitFileName = "sistemo.service"
const unitFilePath = "/etc/systemd/system/" + unitFileName

func serviceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage sistemo systemd service",
	}
	cmd.AddCommand(serviceInstallCmd())
	cmd.AddCommand(serviceUninstallCmd())
	cmd.AddCommand(serviceStatusCmd())
	cmd.AddCommand(serviceLogsCmd())
	return cmd
}

func serviceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install and start sistemo as a systemd service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if syscall.Geteuid() != 0 {
				return fmt.Errorf("must run as root (use sudo)")
			}
			binaryPath, err := os.Executable()
			if err != nil {
				return fmt.Errorf("find executable: %w", err)
			}
			// Resolve symlinks so the unit file has the real path
			if resolved, err := filepath.EvalSymlinks(binaryPath); err == nil {
				binaryPath = resolved
			}
			dataDir := getDataDirFromCmd(cmd)

			// Check if already running (to decide start vs restart)
			wasRunning := exec.Command("systemctl", "is-active", "--quiet", "sistemo").Run() == nil

			unit := generateUnitFile(binaryPath, dataDir)
			if err := os.WriteFile(unitFilePath, []byte(unit), 0644); err != nil {
				return fmt.Errorf("write unit file: %w", err)
			}
			fmt.Printf("Wrote %s\n", unitFilePath)

			startCmd := "start"
			if wasRunning {
				startCmd = "restart"
			}

			for _, cmdArgs := range [][]string{
				{"systemctl", "daemon-reload"},
				{"systemctl", "enable", "sistemo"},
				{"systemctl", startCmd, "sistemo"},
			} {
				c := exec.Command(cmdArgs[0], cmdArgs[1:]...)
				c.Stdout = os.Stdout
				c.Stderr = os.Stderr
				if err := c.Run(); err != nil {
					return fmt.Errorf("%s: %w", strings.Join(cmdArgs, " "), err)
				}
			}
			if wasRunning {
				fmt.Println("Sistemo service updated and restarted.")
			} else {
				fmt.Println("Sistemo service installed and started.")
			}
			fmt.Println("  Status:  systemctl status sistemo")
			fmt.Println("  Logs:    journalctl -u sistemo -f")
			fmt.Println("  Stop:    sudo systemctl stop sistemo")
			return nil
		},
	}
}

func serviceUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the sistemo systemd service",
		RunE: func(cmd *cobra.Command, args []string) error {
			if syscall.Geteuid() != 0 {
				return fmt.Errorf("must run as root (use sudo)")
			}
			exec.Command("systemctl", "stop", "sistemo").Run()
			exec.Command("systemctl", "disable", "sistemo").Run()
			os.Remove(unitFilePath)
			exec.Command("systemctl", "daemon-reload").Run()
			fmt.Println("Sistemo service removed.")
			return nil
		},
	}
}

func serviceStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show sistemo service status",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := exec.Command("systemctl", "status", "sistemo")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			c.Run() // ignore exit code — systemctl returns non-zero for stopped services
			return nil
		},
	}
}

func serviceLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Follow sistemo service logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			c := exec.Command("journalctl", "-u", "sistemo", "-f")
			c.Stdout = os.Stdout
			c.Stderr = os.Stderr
			return c.Run()
		},
	}
}

func generateUnitFile(binaryPath, dataDir string) string {
	return fmt.Sprintf(`[Unit]
Description=Sistemo VM Manager
After=network.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="%s" up --data-dir="%s"
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
`, binaryPath, dataDir)
}
