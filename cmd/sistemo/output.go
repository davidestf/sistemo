package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
)

var outputFormat string // set by global --output flag

func isJSON() bool {
	return outputFormat == "json"
}

func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

func newTabWriter() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
}

func confirmAction(action, target string) bool {
	if outputFormat == "json" {
		return true // non-interactive mode, skip confirmation
	}
	fmt.Fprintf(os.Stderr, "Are you sure you want to %s %s? [y/N] ", action, target)
	var answer string
	fmt.Scanln(&answer)
	return answer == "y" || answer == "Y" || answer == "yes"
}

func colorStatus(status string) string {
	if outputFormat == "json" || !isTTY() {
		return status
	}
	switch status {
	case "running":
		return "\033[32m" + status + "\033[0m" // green
	case "stopped":
		return "\033[33m" + status + "\033[0m" // yellow
	case "error", "failed":
		return "\033[31m" + status + "\033[0m" // red
	case "maintenance":
		return "\033[36m" + status + "\033[0m" // cyan
	case "online":
		return "\033[32m" + status + "\033[0m" // green
	case "attached":
		return "\033[34m" + status + "\033[0m" // blue
	default:
		return status
	}
}

func isTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
