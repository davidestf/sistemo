package main

import (
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
)

func vmExposeCmd() *cobra.Command {
	var portFlag string
	cmd := &cobra.Command{
		Use:   "expose <name|id> --port <[hostPort:]vmPort>",
		Short: "Expose a VM port on the host",
		Long: `Expose a VM port by creating iptables DNAT rules.

Examples:
  sistemo vm expose myvm --port 80           # host:80 → VM:80
  sistemo vm expose myvm --port 8080:80      # host:8080 → VM:80`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			db := getDBFromCmd(cmd)
			return runExpose(logger, db, args[0], portFlag)
		},
	}
	cmd.Flags().StringVar(&portFlag, "port", "", "port mapping: hostPort:vmPort or just port (required)")
	cmd.MarkFlagRequired("port")
	return cmd
}

func vmUnexposeCmd() *cobra.Command {
	var portFlag int
	cmd := &cobra.Command{
		Use:   "unexpose <name|id> --port <hostPort>",
		Short: "Remove a port expose rule",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			db := getDBFromCmd(cmd)
			return runUnexpose(logger, db, args[0], portFlag)
		},
	}
	cmd.Flags().IntVar(&portFlag, "port", 0, "host port to unexpose (required)")
	cmd.MarkFlagRequired("port")
	return cmd
}

// parsePortMapping parses "hostPort:vmPort" or "port" (same for both).
func parsePortMapping(s string) (hostPort, vmPort int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) == 2 {
		hostPort, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid host port %q", parts[0])
		}
		vmPort, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid vm port %q", parts[1])
		}
	} else {
		hostPort, err = strconv.Atoi(parts[0])
		if err != nil {
			return 0, 0, fmt.Errorf("invalid port %q", parts[0])
		}
		vmPort = hostPort
	}
	if hostPort < 1 || hostPort > 65535 || vmPort < 1 || vmPort > 65535 {
		return 0, 0, fmt.Errorf("ports must be 1-65535")
	}
	return hostPort, vmPort, nil
}

func runExpose(logger *zap.Logger, database *sql.DB, nameOrID, portSpec string) error {
	hostPort, vmPort, err := parsePortMapping(portSpec)
	if err != nil {
		return fmt.Errorf("invalid port spec: %w", err)
	}

	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first): %w", err)
	}

	vmID, err := resolveVMID(database, nameOrID)
	if err != nil {
		return err
	}
	if err := daemon.ExposePort(baseURL, vmID, hostPort, vmPort, "tcp"); err != nil {
		return fmt.Errorf("expose port: %w", err)
	}
	fmt.Printf("Exposed host:%d → VM:%d (tcp) on %s\n", hostPort, vmPort, vmID)
	return nil
}

func runUnexpose(logger *zap.Logger, database *sql.DB, nameOrID string, hostPort int) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first): %w", err)
	}

	vmID, err := resolveVMID(database, nameOrID)
	if err != nil {
		return err
	}
	if err := daemon.UnexposePort(baseURL, vmID, hostPort); err != nil {
		return fmt.Errorf("unexpose port: %w", err)
	}
	fmt.Printf("Removed port expose for host:%d on %s\n", hostPort, vmID)
	return nil
}

// resolveVMID looks up a VM by name or ID and returns its ID.
func resolveVMID(database *sql.DB, nameOrID string) (string, error) {
	return lookupVM(database, nameOrID, "destroyed", "error", "failed")
}
