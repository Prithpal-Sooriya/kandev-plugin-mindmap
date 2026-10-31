package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	_ "modernc.org/sqlite"
)

// probeServer is the loopback HTTP+WS server spike check 3/4 relies on. It
// listens on 127.0.0.1:<random port>.
type probeServer struct {
	addr string
	port int
	http *http.Server
}

func (p *mindmapPlugin) ensureServer() (*probeServer, error) {
	p.srvOnce.Do(func() { p.srv, p.srvErr = startProbeServer() })
	return p.srv, p.srvErr
}

func startProbeServer() (*probeServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = io.WriteString(w, "hello from kandev-plugin-mindmap\n")
	})
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteMessage(websocket.TextMessage, []byte("hello:"+r.Header.Get("Origin")))
		for {
			mt, msg, err := c.ReadMessage()
			if err != nil {
				return
			}
			_ = c.WriteMessage(mt, append([]byte("echo:"), msg...))
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	addr := ln.Addr().(*net.TCPAddr)
	return &probeServer{addr: addr.String(), port: addr.Port, http: srv}, nil
}

// sqliteProbe opens a pure-Go sqlite DB in the plugin data dir, does a write
// and a read, and reports the engine version.
func sqliteProbe(dir string) map[string]any {
	out := map[string]any{}
	db, err := sql.Open("sqlite", filepath.Join(dir, "probe.db"))
	if err != nil {
		out["error"] = err.Error()
		return out
	}
	defer db.Close()
	var version string
	if err := db.QueryRow("select sqlite_version()").Scan(&version); err != nil {
		out["error"] = err.Error()
		return out
	}
	out["version"] = version
	if _, err := db.Exec("create table if not exists t (id integer primary key, v text)"); err != nil {
		out["error"] = err.Error()
		return out
	}
	if _, err := db.Exec("insert into t (v) values (?)", "x"); err != nil {
		out["error"] = err.Error()
		return out
	}
	var n int
	_ = db.QueryRow("select count(*) from t").Scan(&n)
	out["rows"] = n
	return out
}

var tunnelURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func runQuickTunnel(ctx context.Context, bin, target string, hold time.Duration) map[string]any {
	out := map[string]any{}
	tctx, cancel := context.WithTimeout(ctx, hold+10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(tctx, bin, "tunnel", "--no-autoupdate", "--url", "http://"+target)
	logPath := filepath.Join(os.TempDir(), "mindmap-cloudflared.log")
	lf, _ := os.Create(logPath)
	if lf != nil {
		defer lf.Close()
		cmd.Stdout, cmd.Stderr = lf, lf
	}
	if err := cmd.Start(); err != nil {
		out["error"] = err.Error()
		return out
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(hold)
	var pub string
	for time.Now().Before(deadline) && pub == "" {
		time.Sleep(500 * time.Millisecond)
		b, _ := os.ReadFile(logPath)
		pub = tunnelURLRe.FindString(string(b))
	}
	if pub == "" {
		out["error"] = "no tunnel URL within hold window"
		return out
	}
	out["public_url"] = pub
	time.Sleep(3 * time.Second) // let DNS/edge settle
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Get(pub + "/hello")
	if err != nil {
		out["fetch_error"] = err.Error()
		return out
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	out["status"] = resp.StatusCode
	out["body"] = strings.TrimSpace(string(b))
	out["note"] = fmt.Sprintf("tunnel killed after fetch; log at %s", logPath)
	return out
}
