package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
	"github.com/davidestf/sistemo/internal/db"
)

func networkCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "network",
		Short: "Manage networks (create, list, delete)",
		Long: `Manage isolated networks for VM-to-VM communication.

VMs on the same network can reach each other. VMs on different networks are isolated.
Without --network, VMs join the default shared bridge (sistemo0).`,
	}
	cmd.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
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
		if database := getDBFromCmd(cmd); database != nil {
			_ = database.Close()
		}
		return nil
	}

	cmd.AddCommand(networkCreateCmd())
	cmd.AddCommand(networkListCmd())
	cmd.AddCommand(networkDeleteCmd())
	return cmd
}

func networkCreateCmd() *cobra.Command {
	var subnet string
	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create an isolated network",
		Long: `Create a named network with its own bridge and subnet.

VMs deployed with --network <name> will join this network and can reach each other.
A /24 subnet is auto-assigned from the 10.201-254.0.0 range.

Examples:
  sistemo network create backend
  sistemo network create db-tier --subnet 10.210.0.0/24`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			database := getDBFromCmd(cmd)
			return runNetworkCreate(logger, database, args[0], subnet)
		},
	}
	cmd.Flags().StringVar(&subnet, "subnet", "", "custom subnet (default: auto-assigned /24)")
	return cmd
}

func networkListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List networks",
		RunE: func(cmd *cobra.Command, _ []string) error {
			database := getDBFromCmd(cmd)
			runNetworkList(database)
			return nil
		},
	}
}

func networkDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a network",
		Long: `Delete a named network. The network must have no running VMs.

Examples:
  sistemo network delete backend`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := getLogger(cmd)
			database := getDBFromCmd(cmd)
			return runNetworkDelete(logger, database, args[0])
		},
	}
}

func runNetworkCreate(logger *zap.Logger, database *sql.DB, name, subnet string) error {
	// Validate name
	if name == "" || name == "default" || name == "sistemo0" {
		return fmt.Errorf("invalid network name %q", name)
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return fmt.Errorf("network name must contain only letters, numbers, hyphens, and underscores")
		}
	}
	if len(name) > 50 {
		return fmt.Errorf("network name too long (max 50 characters)")
	}

	// Check daemon is running (needed to create bridge)
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sudo sistemo up' first): %w", err)
	}

	// Auto-assign subnet if not provided
	if subnet == "" {
		var err error
		subnet, err = findFreeSubnet(database)
		if err != nil {
			return fmt.Errorf("auto-assign subnet: %w", err)
		}
	}

	bridgeName := "br-" + name
	if len(bridgeName) > 15 {
		bridgeName = "br-" + name[:12]
	}

	// Check for bridge name collision from truncation
	var existingNet string
	if database.QueryRow("SELECT name FROM network WHERE bridge_name = ?", bridgeName).Scan(&existingNet) == nil {
		return fmt.Errorf("bridge name %q conflicts with network %q (try a shorter name)", bridgeName, existingNet)
	}

	id := uuid.NewString()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := database.Exec(
		`INSERT INTO network (id, name, subnet, bridge_name, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, name, subnet, bridgeName, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("network %q already exists", name)
		}
		return fmt.Errorf("create network: %w", err)
	}

	// Create the bridge via daemon
	if err := daemon.CreateNetwork(baseURL, name, subnet, bridgeName); err != nil {
		// Rollback DB
		db.SafeExec(database, "DELETE FROM network WHERE id = ?", id)
		return fmt.Errorf("create bridge: %w", err)
	}

	db.LogAction(database, "network.create", "network", id, name, fmt.Sprintf("subnet=%s bridge=%s", subnet, bridgeName), true)
	fmt.Printf("Created network %q (subnet: %s, bridge: %s)\n", name, subnet, bridgeName)
	return nil
}

func runNetworkList(database *sql.DB) {
	rows, err := database.Query("SELECT name, subnet, bridge_name, created_at FROM network ORDER BY created_at")
	if err != nil {
		fmt.Fprintf(os.Stderr, "query networks: %v\n", err)
		return
	}
	defer rows.Close()

	type netRow struct{ name, subnet, bridge, created string }
	var nets []netRow
	for rows.Next() {
		var n netRow
		if rows.Scan(&n.name, &n.subnet, &n.bridge, &n.created) == nil {
			nets = append(nets, n)
		}
	}

	// Always show default network first
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSUBNET\tBRIDGE\tVMs")

	// Count VMs on default network
	var defaultCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM vm WHERE network_id IS NULL AND status NOT IN ('deleted')").Scan(&defaultCount); err != nil {
		defaultCount = 0
	}
	fmt.Fprintf(tw, "default\t10.200.0.0/16\tsistemo0\t%d\n", defaultCount)

	for _, n := range nets {
		var vmCount int
		if err := database.QueryRow("SELECT COUNT(*) FROM vm WHERE network_id = (SELECT id FROM network WHERE name = ?) AND status NOT IN ('deleted')", n.name).Scan(&vmCount); err != nil {
			vmCount = 0
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", n.name, n.subnet, n.bridge, vmCount)
	}
	tw.Flush()
}

func runNetworkDelete(logger *zap.Logger, database *sql.DB, name string) error {
	if name == "default" || name == "sistemo0" {
		return fmt.Errorf("cannot delete the default network")
	}

	var id, subnet, bridgeName string
	err := database.QueryRow("SELECT id, subnet, bridge_name FROM network WHERE name = ?", name).Scan(&id, &subnet, &bridgeName)
	if err == sql.ErrNoRows {
		return fmt.Errorf("network %q not found", name)
	}
	if err != nil {
		return fmt.Errorf("lookup network: %w", err)
	}

	// Check for running VMs
	var vmCount int
	if err := database.QueryRow("SELECT COUNT(*) FROM vm WHERE network_id = ? AND status NOT IN ('deleted')", id).Scan(&vmCount); err != nil {
		return fmt.Errorf("check VMs on network: %w", err)
	}
	if vmCount > 0 {
		return fmt.Errorf("network %q has %d VMs — delete or move them first", name, vmCount)
	}

	// Delete bridge via daemon
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err == nil {
		if err := daemon.DeleteNetwork(baseURL, name); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: bridge removal may have failed: %v\n", err)
		}
	}

	db.SafeExec(database, "DELETE FROM network WHERE id = ?", id)
	db.LogAction(database, "network.delete", "network", id, name, "", true)
	fmt.Printf("Deleted network %q\n", name)
	return nil
}

// findFreeSubnet finds the next available /24 subnet in the 10.201-254.0.0 range.
// Checks BOTH the network DB table AND existing bridges on the system to avoid
// conflicts with stale bridges from previous sessions.
func findFreeSubnet(database *sql.DB) (string, error) {
	used := make(map[string]bool)

	// Check DB
	rows, err := database.Query("SELECT subnet FROM network")
	if err != nil {
		return "", err
	}
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			used[s] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("query subnets: %w", err)
	}

	// Check existing bridges on the system (catches stale bridges not in DB)
	out, err := exec.Command("ip", "-o", "addr", "show", "type", "bridge").Output()
	if err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			// Format: "243: br-db2    inet 10.202.0.1/24 ..."
			fields := strings.Fields(line)
			for i, f := range fields {
				if f == "inet" && i+1 < len(fields) {
					// Parse CIDR, convert gateway to network form
					_, ipNet, err := net.ParseCIDR(fields[i+1])
					if err == nil {
						used[ipNet.String()] = true
					}
				}
			}
		}
	}

	for i := 201; i <= 254; i++ {
		subnet := fmt.Sprintf("10.%d.0.0/24", i)
		if !used[subnet] {
			return subnet, nil
		}
	}
	return "", fmt.Errorf("no free subnets available (all 10.201-254.0.0/24 in use)")
}

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
