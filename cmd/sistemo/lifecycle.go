package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/davidestf/sistemo/internal/daemon"
	"github.com/davidestf/sistemo/internal/db"
)

func runDelete(logger *zap.Logger, database *sql.DB, nameOrID string, preserveStorage bool) error {
	baseURL := daemon.URL()
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	deleted, err := daemon.DeleteVM(baseURL, vmID, preserveStorage)
	if err != nil {
		daemonUnreachable := strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "dial") ||
			strings.Contains(err.Error(), "unreachable") ||
			strings.Contains(err.Error(), "timeout")
		if daemonUnreachable {
			// Daemon is down — update DB directly as fallback
			fmt.Fprintln(os.Stderr, "Warning: daemon unreachable; marking VM as deleted in database.")
			now := time.Now().UTC().Format(time.RFC3339)
			db.SafeExec(database, "UPDATE vm SET status = 'deleted', last_state_change = ? WHERE id = ?", now, vmID)
			db.SafeExec(database, "DELETE FROM ip_allocation WHERE vm_id = ?", vmID)
			db.SafeExec(database, "DELETE FROM port_rule WHERE vm_id = ?", vmID)
			db.SafeExec(database, "UPDATE volume SET status='online', attached=NULL WHERE attached=?", vmID)
		} else {
			return fmt.Errorf("delete VM: %w", err)
		}
	} else if !deleted {
		fmt.Fprintln(os.Stderr, "Warning: VM process not found on daemon (may already be stopped).")
	}
	fmt.Printf("Deleted %s\n", vmID)
	return nil
}

func runStop(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return fmt.Errorf("VM not found: %s", nameOrID)
	}
	stopped, err := daemon.StopVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}
	if !stopped {
		fmt.Printf("VM %s not found on daemon.\n", vmID)
		return nil
	}
	fmt.Printf("Stopped %s\n", vmID)
	return nil
}

func runStart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	resp, err := daemon.StartVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	fmt.Printf("Started %s\n", vmID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	fmt.Printf("  Dashboard: %s/dashboard/#/vms/%s\n", baseURL, vmID)
	return nil
}

func runRestart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	vmID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}

	// Stop (ignore "not found" — VM might already be stopped)
	stopped, err := daemon.StopVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("stop VM: %w", err)
	}
	if stopped {
		fmt.Printf("Stopped %s\n", vmID)
	}

	// Start
	resp, err := daemon.StartVM(baseURL, vmID)
	if err != nil {
		return fmt.Errorf("start VM: %w", err)
	}
	fmt.Printf("Started %s\n", vmID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	return nil
}
