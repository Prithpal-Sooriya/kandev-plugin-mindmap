package web

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// A 64-character asset "hash" with ".." used to make blobPath resolve to
// <dataDir>/secret.txt, which GET /m/<token>/assets/<id> then served.
func TestAssetTraversalNotServed(t *testing.T) {
	e := newEnv(t)
	dataDir := e.st.DataDir()
	if err := os.WriteFile(filepath.Join(dataDir, "secret.txt"), []byte("top secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	tail := "..//" + filepath.Base(dataDir) + "/"
	sha := tail + strings.Repeat("./", (64-len(tail)-len("secret.txt"))/2) + "secret.txt"
	if len(sha) != 64 {
		sha = strings.Replace(sha, "..//", "../", 1)
	}
	if len(sha) != 64 || filepath.Join(dataDir, "assets", sha[:2], sha) != filepath.Join(dataDir, "secret.txt") {
		t.Fatalf("payload %q (%d) does not reach the secret", sha, len(sha))
	}
	asset := contract.Asset{ID: store.NewID(contract.PrefixAsset), NodeID: e.m.RootID, Kind: contract.AssetFile, MIME: "text/plain", Bytes: 10, SHA256: sha}
	op := contract.Op{ID: store.NewID(contract.PrefixOp), MapID: e.m.ID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: asset}}
	resp, body := e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/ops", contract.SubmitRequest{ClientID: "h", Ops: []contract.Op{op}}, nil)
	if resp.StatusCode == 200 && !strings.Contains(string(body), `"error"`) {
		t.Fatalf("traversal attach accepted: %d %s", resp.StatusCode, body)
	}
	resp, body = e.do("GET", "/m/"+e.token+"/assets/"+asset.ID+"?map_id="+e.m.ID, nil, nil)
	if resp.StatusCode == 200 || strings.Contains(string(body), "top secret") {
		t.Fatalf("asset route served a file outside assets/: %d %q", resp.StatusCode, body)
	}
}

// Rotate and stop must revoke a link's tokens before they close its sockets:
// a socket checks its token only when it opens, so one that reconnects in
// between would otherwise stay open after the link is gone.
func TestShareStopRevokesBeforeClosing(t *testing.T) {
	e := newEnv(t)
	e.useTunnel(t, fakeTunnel(t))
	rawOf := func(l contract.ShareLink) string {
		return strings.Trim(strings.TrimPrefix(l.URL, "https://demo.trycloudflare.com/m/"), "/")
	}
	raw := rawOf(e.shareMap())
	resp, body := e.do("POST", "/m/"+raw+"/api/join", map[string]any{"name": "Ada", "color": "#3b82f6"}, nil)
	if resp.StatusCode != 200 || len(resp.Cookies()) != 1 {
		t.Fatalf("join: %d %s", resp.StatusCode, body)
	}
	guestTok := resp.Cookies()[0].Value

	var calls int
	var live []string
	orig := e.srv.links.OnStop
	e.srv.links.OnStop = func(id string) {
		calls++
		for _, tok := range []string{raw, guestTok} {
			if _, err := e.st.LookupToken(tok); err == nil {
				live = append(live, tok[:5])
			}
		}
		orig(id)
	}

	resp, body = e.do("POST", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share/rotate", map[string]any{}, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("rotate: %d %s", resp.StatusCode, body)
	}
	if calls != 1 || len(live) != 0 {
		t.Fatalf("rotate closed sockets with tokens still live: calls=%d live=%v", calls, live)
	}
	var rotated contract.ShareLink
	if err := json.Unmarshal(body, &rotated); err != nil {
		t.Fatal(err)
	}
	raw = rawOf(rotated)
	guestTok = ""
	resp, _ = e.do("DELETE", "/m/"+e.token+"/api/maps/"+e.m.ID+"/share", nil, nil)
	if resp.StatusCode != 204 {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	if calls != 2 || len(live) != 0 {
		t.Fatalf("stop closed sockets with tokens still live: calls=%d live=%v", calls, live)
	}
}
