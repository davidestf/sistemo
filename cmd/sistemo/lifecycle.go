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
	machineID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	deleted, err := daemon.DeleteMachine(baseURL, machineID, preserveStorage)
	if err != nil {
		daemonUnreachable := strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "dial") ||
			strings.Contains(err.Error(), "unreachable") ||
			strings.Contains(err.Error(), "timeout")
		if daemonUnreachable {
			// Daemon is down — update DB directly as fallback
			fmt.Fprintln(os.Stderr, "Warning: daemon unreachable; marking machine as deleted in database.")
			now := time.Now().UTC().Format(time.RFC3339)
			db.SafeExec(database, "UPDATE machine SET status = 'deleted', last_state_change = ? WHERE id = ?", now, machineID)
			db.SafeExec(database, "DELETE FROM ip_allocation WHERE machine_id = ?", machineID)
			db.SafeExec(database, "DELETE FROM port_rule WHERE machine_id = ?", machineID)
			db.SafeExec(database, "UPDATE volume SET status='online', machine_id=NULL WHERE machine_id=?", machineID)
		} else {
			return fmt.Errorf("delete machine: %w", err)
		}
	} else if !deleted {
		fmt.Fprintln(os.Stderr, "Warning: machine process not found on daemon (may already be stopped).")
	}
	fmt.Printf("Deleted %s\n", machineID)
	return nil
}

func runStop(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	machineID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return fmt.Errorf("machine not found: %s", nameOrID)
	}
	stopped, err := daemon.StopMachine(baseURL, machineID)
	if err != nil {
		return fmt.Errorf("stop machine: %w", err)
	}
	if !stopped {
		fmt.Printf("Machine %s not found on daemon.\n", machineID)
		return nil
	}
	fmt.Printf("Stopped %s\n", machineID)
	return nil
}

func runStart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	machineID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}
	resp, err := daemon.StartMachine(baseURL, machineID)
	if err != nil {
		return fmt.Errorf("start machine: %w", err)
	}
	fmt.Printf("Started %s\n", machineID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	fmt.Printf("  Dashboard: %s/dashboard/#/machines/%s\n", baseURL, machineID)
	return nil
}

func runRestart(logger *zap.Logger, database *sql.DB, nameOrID string) error {
	baseURL := daemon.URL()
	if err := daemon.Health(baseURL); err != nil {
		return fmt.Errorf("daemon not reachable (run 'sistemo up' first) url=%s: %w", baseURL, err)
	}
	machineID, err := lookupVM(database, nameOrID, "deleted")
	if err != nil {
		return err
	}

	// Stop (ignore "not found" — machine might already be stopped)
	stopped, err := daemon.StopMachine(baseURL, machineID)
	if err != nil {
		return fmt.Errorf("stop machine: %w", err)
	}
	if stopped {
		fmt.Printf("Stopped %s\n", machineID)
	}

	// Start
	resp, err := daemon.StartMachine(baseURL, machineID)
	if err != nil {
		return fmt.Errorf("start machine: %w", err)
	}
	fmt.Printf("Started %s\n", machineID)
	fmt.Printf("  IP: %s  Namespace: %s  Boot: %dms\n", resp.IPAddress, resp.Namespace, resp.BootTimeMS)
	return nil
}
