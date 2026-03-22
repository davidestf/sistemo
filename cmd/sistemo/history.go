package main

import (
	"database/sql"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func historyCmd() *cobra.Command {
	var limit int
	var action string
	var target string

	cmd := &cobra.Command{
		Use:   "history",
		Short: "Show operation history",
		Long: `Show a log of all sistemo operations (VM create, delete, stop, start, expose, etc).

Examples:
  sistemo history                    # last 20 operations
  sistemo history --limit 50         # last 50 operations
  sistemo history --action create    # filter by action
  sistemo history --target myvm      # filter by VM name or ID`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dataDir := getDataDirFromCmd(cmd)
			db, err := getDB(dataDir)
			if err != nil {
				return fmt.Errorf("open database: %w", err)
			}
			defer db.Close()
			runHistory(db, limit, action, target)
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 20, "number of entries to show")
	cmd.Flags().StringVar(&action, "action", "", "filter by action (create, delete, stop, start, expose, unexpose, network.create, network.delete)")
	cmd.Flags().StringVar(&target, "target", "", "filter by target name or ID")
	return cmd
}

func runHistory(db *sql.DB, limit int, action, target string) {
	if limit <= 0 {
		limit = 20
	}
	query := "SELECT timestamp, action, target_type, target_name, target_id, details, success FROM audit_log WHERE 1=1"
	var args []interface{}

	if action != "" {
		query += " AND action = ?"
		args = append(args, action)
	}
	if target != "" {
		query += " AND (target_name = ? OR target_id = ?)"
		args = append(args, target, target)
	}
	query += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "query audit_log: %v\n", err)
		return
	}
	defer rows.Close()

	type entry struct {
		timestamp, action, targetType, targetName, targetID, details string
		success                                                     bool
	}
	var entries []entry
	for rows.Next() {
		var e entry
		var ts, act, tt sql.NullString
		var tn, tid, det sql.NullString
		var succ int
		if rows.Scan(&ts, &act, &tt, &tn, &tid, &det, &succ) != nil {
			continue
		}
		e.timestamp = ts.String
		e.action = act.String
		e.targetType = tt.String
		e.targetName = tn.String
		e.targetID = tid.String
		e.details = det.String
		e.success = succ == 1
		entries = append(entries, e)
	}

	if len(entries) == 0 {
		fmt.Println("No history entries.")
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TIMESTAMP\tACTION\tTARGET\tDETAILS\tOK")
	for _, e := range entries {
		name := e.targetName
		if name == "" {
			name = e.targetID
		}
		ok := "yes"
		if !e.success {
			ok = "FAIL"
		}
		det := e.details
		if len(det) > 60 {
			det = det[:57] + "..."
		}
		// Shorten timestamp to just time if today
		ts := e.timestamp
		if len(ts) > 19 {
			ts = ts[:19]
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", ts, e.action, name, det, ok)
	}
	tw.Flush()
}
