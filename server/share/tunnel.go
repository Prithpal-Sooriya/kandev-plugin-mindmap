package share

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// defaultBrewDirs are the usual Homebrew prefixes (Apple Silicon, then Intel)
// plus the opt keg path cloudflared sometimes lands in.
var defaultBrewDirs = []string{
	"/opt/homebrew/bin",
	"/usr/local/bin",
	"/opt/homebrew/opt/cloudflared/bin",
}

// watcherScript exits when its parent dies, then kills the tunnel process
// group. It runs in its own process group so it outlives a SIGKILL of the plugin.
// sh -c passes the following words as $0, $1, $2, $3.
const watcherScript = `
parent=$1; pgid=$2; poll=$3
while kill -0 "$parent" 2>/dev/null; do sleep "$poll"; done
kill -TERM -"$pgid" 2>/dev/null || true
sleep 0.3
kill -KILL -"$pgid" 2>/dev/null || true
`

// Manager owns one tunnel subprocess. Every share uses the same loopback
// port, so one process serves every map.
type Manager struct {
	// SkipPath skips PATH and only searches BrewDirs (tests).
	SkipPath bool
	// BrewDirs replaces the default Homebrew locations when non-nil.
	BrewDirs []string
	// ExtraEnv is appended to the tunnel process environment (tests).
	ExtraEnv []string
	// URLWait is how long to wait for the public URL. Zero means 20s.
	URLWait time.Duration
	// Backoff is the delay before a restart. Nil means exponential from 200ms.
	Backoff func(attempt int) time.Duration
	// MaxRestarts is how many unexpected exits to tolerate. Zero means 5.
	MaxRestarts int
	// WatchPoll is the parent-death poll interval. Zero means 1s.
	WatchPoll time.Duration
	// OnURL is called, without the Manager lock, when a public URL is known.
	OnURL func(rawURL string)

	mu       sync.Mutex
	startMu  sync.Mutex
	gen      int
	provider contract.ShareProvider
	port     int
	want     bool
	url      string
	err      error
	pid      int
	watchPID int
	restarts int
	exited   chan struct{}
}

// NewManager builds a manager with production defaults.
func NewManager() *Manager { return &Manager{} }

// Health is the current tunnel.
type Health struct {
	Provider contract.ShareProvider
	URL      string
	Running  bool
	Restarts int
	Err      error
}

// Healthy reports whether the process is up and a URL has been seen.
func (m *Manager) Healthy() bool {
	h := m.Snapshot()
	return h.Running && h.URL != "" && h.Err == nil
}

// Snapshot is a consistent view of the tunnel.
func (m *Manager) Snapshot() Health {
	m.mu.Lock()
	defer m.mu.Unlock()
	running := m.pid > 0 && m.exited != nil && !closed(m.exited) && alive(m.pid)
	return Health{Provider: m.provider, URL: m.url, Running: running, Restarts: m.restarts, Err: m.err}
}

func closed(ch <-chan struct{}) bool {
	if ch == nil {
		return true
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// PID is the tunnel process id, or 0.
func (m *Manager) PID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pid
}

// WatchPID is the parent-death watcher, or 0.
func (m *Manager) WatchPID() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.watchPID
}

// Ensure starts the tunnel when it is not already serving this provider and
// port, and returns the public origin (https://host, no path).
func (m *Manager) Ensure(ctx context.Context, p contract.ShareProvider, port int) (string, error) {
	if _, ok := specOf(p); !ok {
		return "", contract.Errf(contract.ErrBadRequest, "unknown share provider %q", p)
	}
	return m.start(ctx, p, port)
}

// Stop kills the tunnel and its watcher. Safe to call more than once.
func (m *Manager) Stop() {
	m.mu.Lock()
	m.want = false
	m.gen++
	pid, wpid := m.pid, m.watchPID
	exited := m.exited
	m.pid, m.watchPID = 0, 0
	m.url = ""
	m.mu.Unlock()
	killGroup(wpid)
	killGroup(pid)
	if exited != nil {
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
		}
	}
}

func (m *Manager) start(ctx context.Context, p contract.ShareProvider, port int) (string, error) {
	m.startMu.Lock()
	defer m.startMu.Unlock()
	m.mu.Lock()
	same := m.want && m.provider == p && m.port == port && m.url != "" && m.err == nil && m.pid > 0 && !closed(m.exited)
	u := m.url
	m.mu.Unlock()
	if same {
		return originOf(u), nil
	}
	m.Stop()
	max := m.maxRestarts()
	m.mu.Lock()
	m.gen++
	gen := m.gen
	m.provider, m.port, m.want, m.err, m.restarts, m.url = p, port, true, nil, 0, ""
	m.mu.Unlock()

	var (
		raw    string
		exited <-chan struct{}
		err    error
	)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if attempt > max {
				break
			}
			select {
			case <-ctx.Done():
				m.Stop()
				return "", ctx.Err()
			case <-time.After(m.backoff(attempt)):
			}
			m.mu.Lock()
			if m.gen != gen || !m.want {
				m.mu.Unlock()
				return "", context.Canceled
			}
			m.restarts = attempt
			m.mu.Unlock()
		}
		raw, _, exited, err = m.spawn(ctx, p, port)
		if err == nil {
			break
		}
		var missing *NotInstalledError
		if errors.As(err, &missing) || ctx.Err() != nil {
			m.mu.Lock()
			m.err = err
			m.want = false
			m.mu.Unlock()
			m.Stop()
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return "", err
		}
	}
	if err != nil {
		m.mu.Lock()
		m.err = err
		m.want = false
		m.mu.Unlock()
		return "", err
	}
	m.mu.Lock()
	m.url = raw
	m.err = nil
	m.mu.Unlock()
	if m.OnURL != nil {
		m.OnURL(raw)
	}
	go m.supervise(gen, exited, p, port, max)
	return originOf(raw), nil
}

// supervise restarts the process after an unexpected exit, until Stop or the cap.
func (m *Manager) supervise(gen int, exited <-chan struct{}, p contract.ShareProvider, port, max int) {
	ex := exited
	for {
		if ex == nil {
			return
		}
		<-ex
		m.mu.Lock()
		if m.gen != gen || !m.want {
			m.mu.Unlock()
			return
		}
		m.restarts++
		n := m.restarts
		m.mu.Unlock()
		if n > max {
			m.mu.Lock()
			m.err = contract.Errf(contract.ErrUnavailable, "tunnel kept exiting")
			m.want = false
			m.mu.Unlock()
			return
		}
		time.Sleep(m.backoff(n))
		m.mu.Lock()
		if m.gen != gen || !m.want {
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), m.urlWait()+time.Second)
		raw, pid, next, err := m.spawn(ctx, p, port)
		cancel()
		if err != nil {
			ex = nil
			continue
		}
		m.mu.Lock()
		if m.gen != gen || !m.want {
			m.mu.Unlock()
			killGroup(pid)
			return
		}
		m.url = raw
		m.err = nil
		m.mu.Unlock()
		if m.OnURL != nil {
			m.OnURL(raw)
		}
		ex = next
	}
}

// spawn starts the binary and blocks until it prints a public URL.
func (m *Manager) spawn(ctx context.Context, p contract.ShareProvider, port int) (string, int, <-chan struct{}, error) {
	spec, _ := specOf(p)
	bin, err := m.findBin(p)
	if err != nil {
		return "", 0, nil, err
	}
	cmd := exec.Command(bin, spec.args(port)...)
	cmd.Env = append(os.Environ(), m.ExtraEnv...)
	setGroup(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		return "", 0, nil, contract.Errf(contract.ErrUnavailable, "start %s: %v", spec.bin, err)
	}
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = pw.Close()
		close(done)
	}()
	pid := cmd.Process.Pid
	m.mu.Lock()
	m.pid = pid
	m.exited = done
	m.mu.Unlock()
	m.armWatcher(pid)

	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if u := spec.url.FindString(sc.Text()); u != "" {
				select {
				case found <- u:
				default:
				}
			}
		}
	}()

	timer := time.NewTimer(m.urlWait())
	defer timer.Stop()
	select {
	case u := <-found:
		return u, pid, done, nil
	case <-done:
		// The URL line can land in the same instant the process exits.
		select {
		case u := <-found:
			return u, pid, done, nil
		case <-time.After(300 * time.Millisecond):
			m.clearPID(pid)
			return "", 0, nil, contract.Errf(contract.ErrUnavailable, "%s exited before reporting a URL", spec.bin)
		}
	case <-ctx.Done():
		killGroup(pid)
		m.clearPID(pid)
		return "", 0, nil, ctx.Err()
	case <-timer.C:
		killGroup(pid)
		m.clearPID(pid)
		return "", 0, nil, contract.Errf(contract.ErrUnavailable, "timed out waiting for the %s URL", spec.bin)
	}
}

func (m *Manager) clearPID(pid int) {
	m.mu.Lock()
	if m.pid == pid {
		m.pid = 0
	}
	m.mu.Unlock()
}

func (m *Manager) armWatcher(tunnelPID int) {
	if runtime.GOOS == "windows" || tunnelPID <= 0 {
		return
	}
	poll := m.WatchPoll
	if poll <= 0 {
		poll = time.Second
	}
	cmd := exec.Command("/bin/sh", "-c", watcherScript, "watcher",
		strconv.Itoa(os.Getpid()), strconv.Itoa(tunnelPID),
		strconv.FormatFloat(poll.Seconds(), 'f', 3, 64))
	setGroup(cmd)
	if err := cmd.Start(); err != nil {
		return
	}
	m.mu.Lock()
	old := m.watchPID
	m.watchPID = cmd.Process.Pid
	m.mu.Unlock()
	if old != 0 && old != cmd.Process.Pid {
		killGroup(old)
	}
	go func() { _ = cmd.Wait() }()
}

func (m *Manager) maxRestarts() int {
	if m.MaxRestarts > 0 {
		return m.MaxRestarts
	}
	return 5
}

func (m *Manager) backoff(attempt int) time.Duration {
	if m.Backoff != nil {
		return m.Backoff(attempt)
	}
	d := 200 * time.Millisecond
	for i := 1; i < attempt; i++ {
		d *= 2
		if d > 30*time.Second {
			return 30 * time.Second
		}
	}
	return d
}

func (m *Manager) urlWait() time.Duration {
	if m.URLWait > 0 {
		return m.URLWait
	}
	return 20 * time.Second
}

func (m *Manager) brewDirs() []string {
	if m.BrewDirs != nil {
		return m.BrewDirs
	}
	return defaultBrewDirs
}

// findBin looks up the provider binary on PATH, then in Homebrew locations.
func (m *Manager) findBin(p contract.ShareProvider) (string, error) {
	spec, ok := specOf(p)
	if !ok {
		return "", contract.Errf(contract.ErrBadRequest, "unknown share provider %q", p)
	}
	if !m.SkipPath {
		if path, err := exec.LookPath(spec.bin); err == nil {
			return path, nil
		}
	}
	for _, dir := range m.brewDirs() {
		cand := filepath.Join(dir, spec.bin)
		st, err := os.Stat(cand)
		if err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return cand, nil
		}
	}
	return "", &NotInstalledError{Provider: p, Command: spec.install}
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}
