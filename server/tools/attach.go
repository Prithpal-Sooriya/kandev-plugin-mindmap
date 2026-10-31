package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"kandev-plugin-mindmap/server/app"
	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

func appSubmit(a contract.Actor) app.SubmitOptions { return app.SubmitOptions{Actor: a, NoLocks: true} }

// DefaultFileRoots are the directories attach.file_path may read. Kandev does
// not tell plugins a task's worktree path (no field in pluginsdk.Task or
// Session), so the plugin allows Kandev's task workspaces
// ($KANDEV_HOME/tasks, default ~/.kandev/tasks) and the temp dirs agents and
// Playwright write screenshots to. Override with MINDMAP_ATTACH_ROOTS
// (path-list separated).
func DefaultFileRoots() []string {
	if v := os.Getenv("MINDMAP_ATTACH_ROOTS"); v != "" {
		return filepath.SplitList(v)
	}
	var roots []string
	home := os.Getenv("KANDEV_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".kandev")
		}
	}
	if home != "" {
		roots = append(roots, filepath.Join(home, "tasks"))
	}
	roots = append(roots, os.TempDir(), "/tmp", "/private/tmp")
	return roots
}

func (t *Tools) allowedFile(p string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", contract.FieldErr(contract.ErrBadRequest, "file_path", "must be an absolute path (the bridge sends relative files as base64)")
	}
	real, err := filepath.EvalSymlinks(filepath.Clean(p))
	if err != nil {
		return "", contract.FieldErr(contract.ErrFetchFailed, "file_path", "cannot read %s: %v", p, err)
	}
	for _, root := range t.FileRoots {
		r, err := filepath.EvalSymlinks(root)
		if err != nil {
			continue
		}
		if rel, err := filepath.Rel(r, real); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", contract.FieldErr(contract.ErrForbidden, "file_path",
		"only files inside your task worktree (under %s) or a temp dir can be attached; copy it there or use base64", strings.Join(t.FileRoots[:1], ""))
}

// safeDialer refuses loopback, private and link-local addresses so an
// attach URL cannot reach the user's local services.
func safeDialer(allowPrivate bool) *net.Dialer {
	return &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		if allowPrivate {
			return nil
		}
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip := net.ParseIP(host)
		if ip == nil || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("address %s is not public", host)
		}
		return nil
	}}
}

func (t *Tools) fetchURL(ctx context.Context, raw string) ([]byte, string, string, error) {
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DialContext: safeDialer(t.AllowPrivateURLs).DialContext,
		TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 10 * time.Second}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", "", err
	}
	req.Header.Set("User-Agent", "kandev-plugin-mindmap/attach")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, "", "", fmt.Errorf("GET returned %s", resp.Status)
	}
	if resp.ContentLength > contract.MaxAssetBytes {
		return nil, "", "", contract.FieldErr(contract.ErrTooLarge, "url", "file is %d bytes; the limit is %d", resp.ContentLength, contract.MaxAssetBytes)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, contract.MaxAssetBytes+1))
	if err != nil {
		return nil, "", "", err
	}
	if len(data) > contract.MaxAssetBytes {
		return nil, "", "", contract.FieldErr(contract.ErrTooLarge, "url", "file is over %d bytes", contract.MaxAssetBytes)
	}
	name := path.Base(resp.Request.URL.Path)
	return data, resp.Header.Get("Content-Type"), name, nil
}

func (t *Tools) attach(ctx context.Context, s *session, a any) (contract.ToolResult, error) {
	args := a.(*contract.AttachArgs)
	nodeID, err := t.writeNode(s, "node_id", args.NodeID, "")
	if err != nil {
		return contract.ToolResult{}, err
	}
	var data []byte
	declared, filename, sourceURL := args.MIMEType, args.Filename, args.SourceURL
	switch {
	case args.URL != "":
		d, ct, name, err := t.fetchURL(ctx, args.URL)
		if err != nil {
			var ce *contract.Error
			if errors.As(err, &ce) {
				return contract.ToolResult{}, ce
			}
			return contract.ToolResult{}, contract.FieldErr(contract.ErrFetchFailed, "url", "could not fetch: %v", err)
		}
		data = d
		if declared == "" {
			declared = ct
		}
		if filename == "" && name != "/" && name != "." {
			filename = name
		}
		if sourceURL == "" {
			sourceURL = args.URL
		}
	case args.FilePath != "":
		p, err := t.allowedFile(args.FilePath)
		if err != nil {
			return contract.ToolResult{}, err
		}
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrFetchFailed, "file_path", "not a readable file")
		}
		if fi.Size() > contract.MaxAssetBytes {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrTooLarge, "file_path", "file is %d bytes; the limit is %d", fi.Size(), contract.MaxAssetBytes)
		}
		if data, err = os.ReadFile(p); err != nil {
			return contract.ToolResult{}, contract.FieldErr(contract.ErrFetchFailed, "file_path", "%v", err)
		}
		if filename == "" {
			filename = filepath.Base(p)
		}
		if declared == "" {
			declared = mimeByExt(p)
		}
	default:
		data, err = base64.StdEncoding.DecodeString(strings.TrimSpace(args.Base64))
		if err != nil {
			if data, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(args.Base64)); err != nil {
				return contract.ToolResult{}, contract.FieldErr(contract.ErrBadRequest, "base64", "not valid base64")
			}
		}
	}
	mime, ok := store.SniffMIME(data, declared)
	if !ok {
		return contract.ToolResult{}, contract.FieldErr(contract.ErrUnsupportedMedia, "mime_type",
			"type %q is not allowed (allowed: %s)", firstNonEmpty(mime, declared), strings.Join(contract.AllowedAssetMIME, ", "))
	}
	asset, err := t.svc.Store.StoreBlob(data, mime)
	if err != nil {
		return contract.ToolResult{}, err
	}
	if len(filename) > 200 {
		filename = filename[:200]
	}
	asset.NodeID, asset.Filename, asset.Caption, asset.SourceURL, asset.Author = nodeID, filename, args.Caption, sourceURL, s.actor
	if asset.SourceURL != "" && contract.ValidateHTTPURL("source_url", asset.SourceURL) != nil {
		asset.SourceURL = ""
	}
	out, err := t.commit(s, []contract.Op{newOp(s.mapID, &contract.AssetAttachPayload{Asset: asset})}, false)
	if err != nil {
		return contract.ToolResult{}, err
	}
	out.NodeIDs = []string{nodeID}
	t.focus(s, nodeID)
	if out.Applied {
		t.activity(s, contract.ActivityAssetAttached, "attached "+firstNonEmpty(filename, string(asset.Kind)), []string{nodeID}, 1, out.Rev)
	}
	r := outcomeResult(fmt.Sprintf("attached %s (%s, %d bytes) to [%s]", asset.ID, mime, asset.Bytes, nodeID), out)
	r.Structured["asset_id"] = asset.ID
	return r, nil
}

func mimeByExt(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".txt", ".log":
		return "text/plain"
	}
	return ""
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

