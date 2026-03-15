package handlers

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/davidestf/sistemo/internal/agent/config"
	"github.com/davidestf/sistemo/internal/agent/network"
	"github.com/davidestf/sistemo/internal/agent/vm"
	"go.uber.org/zap"
)

// isWebSocketUpgrade returns true if the request is a WebSocket upgrade.
func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

// safeWriter serializes writes to a *websocket.Conn.
type safeWriter struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (sw *safeWriter) WriteMessage(messageType int, data []byte) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.conn.WriteMessage(messageType, data)
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		// Allow localhost origins (self-hosted runs on the same machine)
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true
		}
		return strings.HasPrefix(origin, "http://localhost") ||
			strings.HasPrefix(origin, "http://127.0.0.1") ||
			strings.HasPrefix(origin, "https://localhost") ||
			strings.HasPrefix(origin, "https://127.0.0.1")
	},
}

type Terminal struct {
	mgr    *vm.Manager
	cfg    *config.Config
	logger *zap.Logger
	db     *sql.DB
}

func NewTerminal(mgr *vm.Manager, cfg *config.Config, logger *zap.Logger, db *sql.DB) *Terminal {
	return &Terminal{mgr: mgr, cfg: cfg, logger: logger, db: db}
}

// getNamespaceFromDB returns the namespace for the VM from the vm table, or "" if not found or db is nil.
func (h *Terminal) getNamespaceFromDB(vmID string) string {
	if h.db == nil {
		return ""
	}
	var ns string
	if err := h.db.QueryRow("SELECT namespace FROM vm WHERE id = ? AND status = 'running' AND namespace != '' LIMIT 1", vmID).Scan(&ns); err != nil {
		return ""
	}
	return strings.TrimSpace(ns)
}

// controlMsg is a JSON control message from the client (connect or resize).
type controlMsg struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// TerminalPageOrWebSocket: if the request is a WebSocket upgrade, run WebSocket; otherwise serve an HTML page that opens the terminal via WebSocket (so opening the URL in a browser works).
func (h *Terminal) TerminalPageOrWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "use GET to open the terminal page or to connect via WebSocket")
		return
	}
	if isWebSocketUpgrade(r) {
		h.WebSocket(w, r)
		return
	}
	h.serveTerminalPage(w, r)
}

// serveTerminalPage writes an HTML page that loads xterm.js and opens a WebSocket to this same URL.
func (h *Terminal) serveTerminalPage(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
	if vmID == "" {
		writeError(w, http.StatusNotFound, "VM id required")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	// Use the same origin for WebSocket (replace http with ws)
	wsURL := "ws://" + r.Host + r.URL.Path
	html := terminalPageHTML(wsURL)
	_, _ = w.Write([]byte(html))
}

const terminalPageHTMLTemplate = `<!DOCTYPE html>
<html>
<head>
  <meta charset="utf-8">
  <title>Sistemo Terminal</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.css" />
  <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.js"></script>
  <style>
    html, body { margin: 0; padding: 0; height: 100%%; width: 100%%; overflow: hidden; background: #1e1e1e; }
    #term { height: 100vh; width: 100vw; display: block; }
  </style>
</head>
<body>
  <div id="term"></div>
  <script>
    (function() {
      var wsUrl = %q;
      var term = new Terminal({ cursorBlink: true, theme: { background: '#1e1e1e' } });
      term.open(document.getElementById('term'));
      var fitAddon = new FitAddon.FitAddon();
      term.loadAddon(fitAddon);
      fitAddon.fit();
      var ws = new WebSocket(wsUrl);
      ws.binaryType = 'arraybuffer';
      term.onData(function(data) {
        if (ws.readyState === 1) ws.send(data);
      });
      term.onResize(function(size) {
        if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', rows: size.rows, cols: size.cols }));
      });
      window.addEventListener('resize', function() {
        fitAddon.fit();
        if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      });
      ws.onopen = function() {
        fitAddon.fit();
        ws.send(JSON.stringify({ type: 'connect', rows: term.rows, cols: term.cols }));
      };
      ws.onmessage = function(ev) {
        if (typeof ev.data === 'string') return;
        var buf = new Uint8Array(ev.data);
        term.write(buf);
      };
      ws.onclose = function() { term.writeln('\\r\\n\\x1b[31m[Connection closed]\\x1b[m'); };
      ws.onerror = function() { term.writeln('\\r\\n\\x1b[31m[WebSocket error]\\x1b[m'); };
    })();
  </script>
</body>
</html>
`

func terminalPageHTML(wsURL string) string {
	return fmt.Sprintf(terminalPageHTMLTemplate, wsURL)
}

func (h *Terminal) WebSocket(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")

	ns := h.mgr.GetVMNamespace(vmID)
	if ns == "" && h.db != nil {
		ns = h.getNamespaceFromDB(vmID)
	}
	vmip := h.mgr.GetVMIP(vmID)
	if vmip == "" && ns != "" {
		vmip = network.VMIP
	}
	if ns == "" || vmip == "" {
		writeError(w, http.StatusNotFound, "VM not found or not running")
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("websocket upgrade failed", zap.Error(err))
		return
	}
	defer conn.Close()

	// Clear HTTP deadlines for long-lived WebSocket
	conn.UnderlyingConn().SetReadDeadline(time.Time{})
	conn.UnderlyingConn().SetWriteDeadline(time.Time{})

	h.logger.Info("terminal websocket connected", zap.String("vmip", vmip), zap.String("namespace", ns))

	// Wait for first message (connect with rows/cols) before starting SSH
	initialRows := uint16(24)
	initialCols := uint16(80)

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, firstMsg, err := conn.ReadMessage()
	conn.SetReadDeadline(time.Time{})
	if err != nil {
		h.logger.Error("failed to read initial connect message", zap.Error(err))
		return
	}

	var ctrl controlMsg
	if json.Unmarshal(firstMsg, &ctrl) == nil && ctrl.Rows > 0 && ctrl.Cols > 0 {
		initialRows = ctrl.Rows
		initialCols = ctrl.Cols
	}

	h.logger.Info("terminal initial size", zap.Uint16("rows", initialRows), zap.Uint16("cols", initialCols))

	sshArgs := []string{
		"netns", "exec", ns, "ssh",
		"-i", h.cfg.SSHKeyPath,
		"-o", "StrictHostKeyChecking=no",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
		"-o", "PreferredAuthentications=publickey",
		"-o", "GSSAPIAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PasswordAuthentication=no",
		"-o", "AddressFamily=inet",
		"-o", "ConnectTimeout=8",
		"-o", "IPQoS=none",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
		fmt.Sprintf("%s@%s", h.cfg.SSHUser, vmip),
	}

	sshCmd := exec.Command("ip", sshArgs...)
	// LANG/LC_ALL=C avoids "cannot change locale (en_GB.UTF-8)" in minimal VM images that don't have that locale generated.
	sshCmd.Env = append(os.Environ(), "TERM=xterm-256color", "LANG=C", "LC_ALL=C")

	// Start SSH under a PTY with the client's initial terminal size
	ptmx, err := pty.StartWithSize(sshCmd, &pty.Winsize{
		Rows: initialRows,
		Cols: initialCols,
	})
	if err != nil {
		h.logger.Error("failed to start SSH with PTY", zap.Error(err))
		conn.WriteMessage(websocket.TextMessage, []byte("Failed to connect to VM: "+err.Error()))
		return
	}
	defer ptmx.Close()

	pid := sshCmd.Process.Pid
	defer func() {
		syscall.Kill(-pid, syscall.SIGKILL)
		sshCmd.Wait()
	}()

	sw := &safeWriter{conn: conn}

	// Forward PTY output to WebSocket (single goroutine for merged stdout+stderr)
	ptyDone := make(chan struct{})
	go func() {
		defer close(ptyDone)
		buf := make([]byte, 8192)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				if werr := sw.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				if err != io.EOF {
					h.logger.Debug("pty read ended", zap.Error(err))
				}
				return
			}
		}
	}()

	// Forward WebSocket input to PTY, handle resize messages
	wsDone := make(chan struct{})
	go func() {
		defer close(wsDone)
		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				return
			}
			if msgType == websocket.TextMessage || msgType == websocket.BinaryMessage {
				var ctrl controlMsg
				if json.Unmarshal(msg, &ctrl) == nil && (ctrl.Type == "resize" || ctrl.Type == "connect") {
					if ctrl.Rows > 0 && ctrl.Cols > 0 {
						pty.Setsize(ptmx, &pty.Winsize{Rows: ctrl.Rows, Cols: ctrl.Cols})
					}
					continue
				}
				ptmx.Write(msg)
			}
		}
	}()

	// Wait for either side to finish
	select {
	case <-ptyDone:
		h.logger.Info("SSH/PTY process exited")
	case <-wsDone:
		h.logger.Info("WebSocket closed")
	}
}

func (h *Terminal) CreateSession(w http.ResponseWriter, r *http.Request) {
	vmID := chi.URLParam(r, "vmID")
	host := r.Host
	if host == "" {
		host = fmt.Sprintf("localhost:%d", h.cfg.Port)
	}
	ws := fmt.Sprintf("ws://%s/terminals/vm/%s", host, vmID)
	writeJSON(w, http.StatusOK, map[string]string{"ws_url": ws, "vm_id": vmID})
}
