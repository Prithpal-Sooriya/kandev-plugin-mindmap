package share

import (
	"context"
	"errors"
	"net/url"
	"sync"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// Links is the share-token lifecycle: mint, rotate, stop, and the tunnel
// that carries them. Raw share tokens are kept in Secrets so the host can
// be shown the URL again; the tokens table stores only hashes.
type Links struct {
	Store   *store.Store
	Tunnels *Manager
	Secrets *Secrets
	// Port is the loopback port the tunnel forwards to.
	Port func() int

	mu sync.Mutex
}

// NewLinks wires a manager at the store's data directory.
func NewLinks(st *store.Store, m *Manager) (*Links, error) {
	if m == nil {
		m = NewManager()
	}
	sec, err := OpenSecrets(st.DataDir())
	if err != nil {
		return nil, err
	}
	l := &Links{Store: st, Tunnels: m, Secrets: sec, Port: func() int { return 0 }}
	m.OnURL = func(raw string) { _ = st.SetActiveHosts(hostOf(raw)) }
	return l, nil
}

// Shutdown stops the tunnel. Share rows are left as they are so a restart
// can bring the same tokens back on a new hostname.
func (l *Links) Shutdown() {
	if l != nil && l.Tunnels != nil {
		l.Tunnels.Stop()
	}
}

// Share starts the tunnel if needed and returns the public URL for one map.
// A map that is already shared returns its current link.
func (l *Links) Share(ctx context.Context, mapID, by string) (contract.ShareLink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if row, ok, err := l.Store.LiveShare(mapID); err != nil {
		return contract.ShareLink{}, err
	} else if ok && row.Status == contract.ShareActive {
		return l.ensureAndView(ctx, row)
	} else if ok {
		_ = l.stopRow(row)
	}
	return l.open(ctx, mapID, by)
}

// Rotate invalidates the current link and mints a new one. The tunnel stays up.
func (l *Links) Rotate(ctx context.Context, mapID, by string) (contract.ShareLink, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if row, ok, err := l.Store.LiveShare(mapID); err != nil {
		return contract.ShareLink{}, err
	} else if ok {
		if err := l.stopRow(row); err != nil {
			return contract.ShareLink{}, err
		}
	}
	return l.open(ctx, mapID, by)
}

// Stop invalidates the map's link and stops the tunnel when nothing is shared.
func (l *Links) Stop(mapID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	row, ok, err := l.Store.LiveShare(mapID)
	if err != nil || !ok {
		return err
	}
	if err := l.stopRow(row); err != nil {
		return err
	}
	return l.stopTunnelIfIdle()
}

// Kill stops every share, revokes share and guest tokens, and stops the tunnel.
// The caller stops runs (app.Service.StopAllRuns) after this returns.
func (l *Links) Kill() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	rows, err := l.Store.ListLiveShares()
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := l.stopRow(row); err != nil {
			return err
		}
	}
	if err := l.Store.RevokeTokensByKind(contract.TokenShare, contract.TokenGuest); err != nil {
		return err
	}
	l.Tunnels.Stop()
	return nil
}

// Status is the host view of one map's link. ok is false when it is not shared.
func (l *Links) Status(mapID string) (contract.ShareLink, bool, error) {
	row, ok, err := l.Store.LiveShare(mapID)
	if err != nil || !ok {
		return contract.ShareLink{}, ok, err
	}
	return l.view(row), true, nil
}

func (l *Links) open(ctx context.Context, mapID, by string) (contract.ShareLink, error) {
	cfg := LoadSettings(l.Store)
	if _, err := l.Tunnels.findBin(cfg.Provider); err != nil {
		return contract.ShareLink{}, asWire(err)
	}
	id := store.NewID(contract.PrefixShare)
	if err := l.Store.InsertShare(id, mapID, cfg.Provider, by); err != nil {
		return contract.ShareLink{}, err
	}
	raw, info, err := l.Store.IssueToken(contract.TokenShare, 0, mapID, "", "", id, "", 0)
	if err != nil {
		_ = l.Store.FailShare(id, err.Error())
		return contract.ShareLink{}, err
	}
	if err := l.Store.SetShareToken(id, info.ID); err != nil {
		_ = l.Store.RevokeShareLinkTokens(id)
		_ = l.Store.FailShare(id, err.Error())
		return contract.ShareLink{}, err
	}
	if err := l.Secrets.Put(id, raw); err != nil {
		_ = l.Store.RevokeShareLinkTokens(id)
		_ = l.Store.FailShare(id, err.Error())
		return contract.ShareLink{}, err
	}
	origin, err := l.Tunnels.Ensure(ctx, cfg.Provider, l.port())
	if err != nil {
		_ = l.Store.RevokeShareLinkTokens(id)
		_ = l.Secrets.Delete(id)
		msg := err.Error()
		_ = l.Store.FailShare(id, msg)
		_ = l.stopTunnelIfIdle()
		return contract.ShareLink{}, asWire(err)
	}
	if err := l.Store.ActivateShare(id, hostOf(origin)); err != nil {
		return contract.ShareLink{}, err
	}
	row, _, err := l.Store.LiveShare(mapID)
	if err != nil {
		return contract.ShareLink{}, err
	}
	return l.view(row), nil
}

func (l *Links) ensureAndView(ctx context.Context, row store.ShareRow) (contract.ShareLink, error) {
	cfg := LoadSettings(l.Store)
	provider := row.Provider
	if !provider.Valid() {
		provider = cfg.Provider
	}
	origin, err := l.Tunnels.Ensure(ctx, provider, l.port())
	if err != nil {
		return contract.ShareLink{}, asWire(err)
	}
	if host := hostOf(origin); host != "" && host != row.PublicHost {
		_ = l.Store.ActivateShare(row.ID, host)
		row.PublicHost = host
		row.Status = contract.ShareActive
	}
	return l.view(row), nil
}

func (l *Links) stopRow(row store.ShareRow) error {
	if err := l.Store.RevokeShareLinkTokens(row.ID); err != nil {
		return err
	}
	if err := l.Secrets.Delete(row.ID); err != nil {
		return err
	}
	return l.Store.StopShare(row.ID)
}

func (l *Links) stopTunnelIfIdle() error {
	n, err := l.Store.CountLiveShares()
	if err != nil {
		return err
	}
	if n == 0 {
		l.Tunnels.Stop()
	}
	return nil
}

func (l *Links) view(row store.ShareRow) contract.ShareLink {
	link := row.ShareLink
	if row.PublicHost != "" {
		if raw, ok := l.Secrets.Get(row.ID); ok {
			link.URL = "https://" + row.PublicHost + "/m/" + raw + "/"
		}
	}
	if link.Status == contract.ShareError {
		link.Install = InstallCommand(row.Provider)
	}
	return link
}

func (l *Links) port() int {
	if l.Port == nil {
		return 0
	}
	return l.Port()
}

func hostOf(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

func asWire(err error) error {
	var missing *NotInstalledError
	if errors.As(err, &missing) {
		return missing.ContractError()
	}
	return err
}
