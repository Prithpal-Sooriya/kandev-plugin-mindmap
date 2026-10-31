//go:build unix

package share

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

func TestMain(m *testing.M) {
	if os.Getenv("MM_ORPHAN_CHILD") == "1" {
		if err := orphanChild(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestNotInstalled(t *testing.T) {
	m := &Manager{SkipPath: true, BrewDirs: []string{}, URLWait: time.Second}
	_, err := m.Ensure(context.Background(), contract.ShareCloudflared, 9)
	var missing *NotInstalledError
	if !errors.As(err, &missing) {
		t.Fatalf("got %v", err)
	}
	if missing.Command != "brew install cloudflared" || !strings.Contains(err.Error(), "brew install cloudflared") {
		t.Fatalf("install hint: %v", err)
	}
	if ce := missing.ContractError(); ce.Code != contract.ErrNotInstalled {
		t.Fatalf("code %s", ce.Code)
	}
}

func TestProviderArgvAndURL(t *testing.T) {
	dir := t.TempDir()
	writeFake(t, dir)
	cases := []struct {
		p    contract.ShareProvider
		host string
		want []string
	}{
		{contract.ShareCloudflared, "demo.trycloudflare.com", []string{"tunnel", "--url", "http://127.0.0.1:40123"}},
		{contract.ShareTailscale, "funnel.tailnet.ts.net", []string{"funnel", "40123"}},
		{contract.ShareNgrok, "abc.ngrok-free.app", []string{"http", "http://127.0.0.1:40123", "--log=stdout"}},
	}
	for _, c := range cases {
		t.Run(string(c.p), func(t *testing.T) {
			argv := filepath.Join(dir, string(c.p)+".argv")
			m := testMgr(dir, []string{"MM_HOST=" + c.host, "MM_ARGV=" + argv})
			t.Cleanup(m.Stop)
			origin, err := m.Ensure(context.Background(), c.p, 40123)
			if err != nil {
				t.Fatal(err)
			}
			if origin != "https://"+c.host {
				t.Fatalf("origin %s", origin)
			}
			got, err := os.ReadFile(argv)
			if err != nil {
				t.Fatal(err)
			}
			if strings.TrimSpace(string(got)) != strings.Join(c.want, "\n") {
				t.Fatalf("argv:\n%s\nwant:\n%s", got, strings.Join(c.want, "\n"))
			}
		})
	}
}

func TestStopKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	writeFake(t, dir)
	pidFile := filepath.Join(dir, "self.pid")
	grand := filepath.Join(dir, "grand.pid")
	m := testMgr(dir, []string{"MM_HOST=demo.trycloudflare.com", "MM_PID=" + pidFile, "MM_GRAND=" + grand})
	if _, err := m.Ensure(context.Background(), contract.ShareCloudflared, 9); err != nil {
		t.Fatal(err)
	}
	self, g := waitPID(t, pidFile), waitPID(t, grand)
	if m.PID() != self {
		t.Fatalf("manager pid %d, script pid %d", m.PID(), self)
	}
	m.Stop()
	waitDead(t, self)
	waitDead(t, g)
	if m.PID() != 0 || m.Healthy() {
		t.Fatalf("still up pid=%d healthy=%v", m.PID(), m.Healthy())
	}
}

func TestRestartAfterExit(t *testing.T) {
	dir := t.TempDir()
	writeFake(t, dir)
	count := filepath.Join(dir, "count")
	m := testMgr(dir, []string{"MM_HOST=demo.trycloudflare.com", "MM_EXIT=1", "MM_COUNT=" + count})
	m.MaxRestarts = 4
	t.Cleanup(m.Stop)
	if _, err := m.Ensure(context.Background(), contract.ShareCloudflared, 9); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if n := readCount(count); n >= 2 {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatalf("restarts did not happen, count=%d snapshot=%+v", readCount(count), m.Snapshot())
}

func TestEnsureIdempotent(t *testing.T) {
	dir := t.TempDir()
	writeFake(t, dir)
	count := filepath.Join(dir, "count")
	m := testMgr(dir, []string{"MM_HOST=demo.trycloudflare.com", "MM_COUNT=" + count})
	t.Cleanup(m.Stop)
	ctx := context.Background()
	a, err := m.Ensure(ctx, contract.ShareCloudflared, 9)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(ctx, contract.ShareCloudflared, 9)
	if err != nil {
		t.Fatal(err)
	}
	if a != b || readCount(count) != 1 {
		t.Fatalf("origin %s %s count %d", a, b, readCount(count))
	}
}

func TestWatcherKillsTunnelWhenParentDies(t *testing.T) {
	dir := t.TempDir()
	writeFake(t, dir)
	cmd := exec.Command(os.Args[0], "-test.run=^TestWatcherKillsTunnelWhenParentDies$", "-test.count=1")
	cmd.Env = append(os.Environ(), "MM_ORPHAN_CHILD=1", "MM_DIR="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child: %v\n%s", err, out)
	}
	self := waitPID(t, filepath.Join(dir, "self.pid"))
	grand := waitPID(t, filepath.Join(dir, "grand.pid"))
	t.Cleanup(func() {
		_ = syscall.Kill(-self, syscall.SIGKILL)
		_ = syscall.Kill(self, syscall.SIGKILL)
		_ = syscall.Kill(grand, syscall.SIGKILL)
	})
	waitDead(t, self)
	waitDead(t, grand)
}

func orphanChild() error {
	dir := os.Getenv("MM_DIR")
	m := testMgr(dir, []string{
		"MM_HOST=demo.trycloudflare.com",
		"MM_PID=" + filepath.Join(dir, "self.pid"),
		"MM_GRAND=" + filepath.Join(dir, "grand.pid"),
	})
	m.WatchPoll = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := m.Ensure(ctx, contract.ShareCloudflared, 9); err != nil {
		return err
	}
	waitFile(filepath.Join(dir, "self.pid"))
	waitFile(filepath.Join(dir, "grand.pid"))
	// Exit without Stop. The watcher, in its own process group, kills the tunnel.
	return nil
}

func testMgr(dir string, env []string) *Manager {
	return &Manager{
		SkipPath:  true,
		BrewDirs:  []string{dir},
		ExtraEnv:  env,
		URLWait:   2 * time.Second,
		Backoff:   func(int) time.Duration { return 20 * time.Millisecond },
		WatchPoll: 50 * time.Millisecond,
	}
}

func writeFake(t *testing.T, dir string) {
	t.Helper()
	body := []byte(`#!/bin/sh
if [ -n "${MM_ARGV:-}" ]; then
  printf '%s\n' "$@" > "$MM_ARGV"
fi
echo "https://${MM_HOST:-demo.trycloudflare.com}"
if [ -n "${MM_GRAND:-}" ]; then
  sleep 120 &
  echo $! > "$MM_GRAND"
fi
if [ -n "${MM_PID:-}" ]; then
  echo $$ > "$MM_PID"
fi
if [ -n "${MM_COUNT:-}" ]; then
  n=0
  if [ -f "$MM_COUNT" ]; then n=$(cat "$MM_COUNT" 2>/dev/null || echo 0); fi
  echo $((n + 1)) > "$MM_COUNT"
fi
if [ "${MM_EXIT:-}" = "1" ]; then
  exit 0
fi
while true; do sleep 1; done
`)
	for _, name := range []string{"cloudflared", "tailscale", "ngrok"} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, body, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func waitPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			n, err := strconv.Atoi(strings.TrimSpace(string(b)))
			if err == nil && n > 0 {
				return n
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no pid in %s", path)
	return 0
}

func waitFile(path string) {
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if st, err := os.Stat(path); err == nil && st.Size() > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive", pid)
}

func readCount(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}
