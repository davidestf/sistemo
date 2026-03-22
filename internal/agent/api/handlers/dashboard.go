package handlers

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"go.uber.org/zap"
)

type Dashboard struct {
	db     *sql.DB
	logger *zap.Logger
}

func NewDashboard(db *sql.DB, logger *zap.Logger) *Dashboard {
	return &Dashboard{db: db, logger: logger}
}

func (h *Dashboard) Dashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	var rows *sql.Rows
	var err error
	if h.db != nil {
		rows, err = h.db.Query(`
			SELECT id, name, status, image, ip_address
			FROM vm
			WHERE status != 'deleted'
			ORDER BY last_state_change DESC
		`)
		if err != nil {
			h.logger.Error("dashboard query failed", zap.Error(err))
			fmt.Fprint(w, dashboardHTML("Error", "<p>Failed to load VMs.</p>"))
			return
		}
		defer rows.Close()
	}

	var sb strings.Builder
	sb.WriteString(`<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Sistemo Dashboard</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 1rem 2rem; background: #0f0f12; color: #e4e4e7; }
    h1 { font-weight: 600; }
    a { color: #7dd3fc; }
    table { border-collapse: collapse; margin-top: 1rem; }
    th, td { padding: 0.5rem 1rem; text-align: left; border-bottom: 1px solid #27272a; }
    th { color: #a1a1aa; font-weight: 500; }
    .status { font-size: 0.875rem; }
    .status.running { color: #4ade80; }
    .status.maintenance { color: #fbbf24; }
    .status.error { color: #f87171; }
  </style>
</head>
<body>
  <h1>Sistemo VMs</h1>
  <p><a href="/health">Health</a> | <a href="/vms">API: List VMs (JSON)</a></p>
  <table>
    <thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Image</th><th>IP</th><th>Terminal</th></tr></thead>
    <tbody>
`)

	if h.db != nil && rows != nil {
		for rows.Next() {
			var id, name, status, image, ip sql.NullString
			if err := rows.Scan(&id, &name, &status, &image, &ip); err != nil {
				continue
			}
			idStr := id.String
			nameStr := name.String
			statusStr := status.String
			imageStr := image.String
			ipStr := ip.String
			if idStr == "" {
				idStr = "-"
			}
			if nameStr == "" {
				nameStr = "-"
			}
			if statusStr == "" {
				statusStr = "-"
			}
			if imageStr == "" {
				imageStr = "-"
			}
			if ipStr == "" {
				ipStr = "-"
			}
			termLink := fmt.Sprintf("/terminals/vm/%s", idStr)
			sb.WriteString(fmt.Sprintf(
				"    <tr><td>%s</td><td>%s</td><td class=\"status %s\">%s</td><td>%s</td><td>%s</td><td><a href=\"%s\">Open</a></td></tr>\n",
				escapeHTML(idStr), escapeHTML(nameStr), strings.ToLower(statusStr), escapeHTML(statusStr), escapeHTML(imageStr), escapeHTML(ipStr), termLink,
			))
		}
	}

	sb.WriteString(`    </tbody>
  </table>
</body>
</html>`)

	fmt.Fprint(w, sb.String())
}

func dashboardHTML(title, body string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>%s</title></head><body>%s</body></html>`, escapeHTML(title), body)
}

func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
