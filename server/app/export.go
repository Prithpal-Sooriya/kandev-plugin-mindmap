package app

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"kandev-plugin-mindmap/server/contract"
)

// DefaultExportDir is where compiled docs go when neither the plugin config
// nor the map names a folder.
const DefaultExportDir = "~/Kandev/mindmaps"

// MaxExportAssetBytes caps the images copied into one export.
const MaxExportAssetBytes = 64 << 20

// DocFile is one file of an export, with a slash path relative to the
// export folder ("<slug>.md", "<slug>/index.md", ...).
type DocFile struct {
	Path string
	Data []byte
}

// docRoots are doc nodes whose parent is not a doc: the root of each
// compiled tree.
func docRoots(nodes map[string]*contract.Node) []*contract.Node {
	var out []*contract.Node
	for _, n := range nodes {
		if n.Deleted || n.Kind != contract.KindDoc {
			continue
		}
		if p := nodes[n.ParentID]; p == nil || p.Kind != contract.KindDoc || p.Deleted {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b *contract.Node) int {
		if c := compiledAt(b).Compare(compiledAt(a)); c != 0 {
			return c
		}
		return int(b.Rev - a.Rev)
	})
	return out
}

func compiledAt(n *contract.Node) time.Time {
	if n.Doc != nil && !n.Doc.CompiledAt.IsZero() {
		return n.Doc.CompiledAt
	}
	return n.CreatedAt
}

// docSlug is a doc tree's slug: from the compile record, else its single
// doc's "<slug>.md", else its title.
func (s *Service) docSlug(root *contract.Node, kids map[string][]*contract.Node) (string, string) {
	if rec, ok := s.loadDocRecord(root.ID); ok && rec.Slug != "" {
		return rec.Slug, rec.SingleDocID
	}
	for _, c := range kids[root.ID] {
		if c.Kind == contract.KindDoc && c.Doc != nil && !strings.Contains(c.Doc.Path, "/") && c.Doc.Path != "index.md" {
			if sl := Slugify(strings.TrimSuffix(c.Doc.Path, ".md")); sl != "" && sl+".md" == c.Doc.Path {
				return sl, c.ID
			}
		}
	}
	sl := Slugify(strings.TrimSuffix(root.Title, " · docs"))
	if sl == "" {
		sl = "mindmap"
	}
	return sl, ""
}

// DocTrees lists the compiled doc trees of a map, newest first.
func (s *Service) DocTrees(mapID string) ([]contract.DocTree, error) {
	out := []contract.DocTree{}
	err := s.Store.View(mapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		kids := childIndex(nodes)
		for _, root := range docRoots(nodes) {
			out = append(out, s.docTree(mapID, root, kids))
		}
		return nil
	})
	return out, err
}

func (s *Service) docTree(mapID string, root *contract.Node, kids map[string][]*contract.Node) contract.DocTree {
	slug, singleID := s.docSlug(root, kids)
	t := contract.DocTree{MapID: mapID, Slug: slug, RootNodeID: root.ParentID}
	if rec, ok := s.loadDocRecord(root.ID); ok {
		t.ExportedTo = rec.ExportedTo
	}
	var entry func(n *contract.Node, depth int) contract.DocEntry
	entry = func(n *contract.Node, depth int) contract.DocEntry {
		e := contract.DocEntry{NodeID: n.ID, Title: n.Title, Words: countWords(n.BodyMD)}
		if n.Doc != nil {
			e.Path = n.Doc.Path
		}
		for _, c := range kids[n.ID] {
			if c.Kind != contract.KindDoc || depth > 16 {
				continue
			}
			if n == root && c.ID == singleID {
				single := entry(c, depth+1)
				t.Single = &single
				continue
			}
			e.Children = append(e.Children, entry(c, depth+1))
		}
		return e
	}
	t.Index = entry(root, 0)
	if t.RootNodeID == "" {
		t.RootNodeID = root.ID
	}
	return t
}

// findDocRoot resolves docRootID ("" = the newest compile) to a doc root.
func findDocRoot(nodes map[string]*contract.Node, docRootID string) (*contract.Node, error) {
	roots := docRoots(nodes)
	if docRootID == "" {
		if len(roots) == 0 {
			return nil, contract.Errf(contract.ErrNotFound, "this map has no compiled docs yet")
		}
		return roots[0], nil
	}
	n := nodes[docRootID]
	for n != nil && n.Kind == contract.KindDoc {
		if p := nodes[n.ParentID]; p == nil || p.Kind != contract.KindDoc || p.Deleted {
			break
		}
		n = nodes[n.ParentID]
	}
	if n == nil || n.Deleted || n.Kind != contract.KindDoc {
		return nil, contract.FieldErr(contract.ErrNotFound, "node_id", "node %s is not a compiled doc", docRootID)
	}
	return n, nil
}

// ExportFiles renders a doc tree (docRootID, or any doc inside it; "" =
// the newest) as files: "<slug>.md", "<slug>/index.md", the section and
// sub-docs at their paths under "<slug>/", and the images of the docs and
// their source nodes under "<slug>/assets/". A doc whose path is missing,
// invalid or taken gets one derived from its place in the tree.
func (s *Service) ExportFiles(mapID, docRootID string) (contract.DocTree, []DocFile, error) {
	var tree contract.DocTree
	var files []DocFile
	var assetIDs []string
	err := s.Store.View(mapID, func(_ contract.Map, nodes map[string]*contract.Node, _ map[string]*contract.Link) error {
		root, err := findDocRoot(nodes, docRootID)
		if err != nil {
			return err
		}
		kids := childIndex(nodes)
		tree = s.docTree(mapID, root, kids)
		slug := tree.Slug
		used := map[string]bool{}
		seenAsset := map[string]bool{}
		addAssets := func(n *contract.Node) {
			for _, a := range n.Assets {
				if !seenAsset[a.ID] {
					seenAsset[a.ID] = true
					assetIDs = append(assetIDs, a.ID)
				}
			}
		}
		var walk func(n *contract.Node, parentFile string, depth int)
		walk = func(n *contract.Node, parentFile string, depth int) {
			var docKids []*contract.Node
			for _, c := range kids[n.ID] {
				if c.Kind == contract.KindDoc {
					docKids = append(docKids, c)
				}
			}
			file := ""
			isSingle := tree.Single != nil && n.ID == tree.Single.NodeID
			switch {
			case isSingle:
				file = slug + ".md"
			case n.Doc != nil && contract.ValidateDocPath("path", n.Doc.Path) == nil && n.Doc.Path != slug+".md":
				file = slug + "/" + n.Doc.Path
			}
			if file == "" || used[file] {
				file = derivedPath(slug, n, parentFile, depth, len(docKids) > 0, used)
			}
			used[file] = true
			files = append(files, DocFile{Path: file, Data: []byte(docMarkdown(n))})
			addAssets(n)
			if n.Doc != nil {
				for _, id := range n.Doc.SourceIDs {
					if src := nodes[id]; src != nil && !src.Deleted {
						addAssets(src)
					}
				}
			}
			if depth > 16 {
				return
			}
			for _, c := range docKids {
				walk(c, file, depth+1)
			}
		}
		walk(root, "", 0)
		return nil
	})
	if err != nil {
		return tree, nil, err
	}
	total := 0
	for _, id := range assetIDs {
		a, p, err := s.Store.AssetFile(mapID, id)
		if err != nil {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil || total+len(b) > MaxExportAssetBytes {
			continue
		}
		total += len(b)
		name := a.ID
		if f := Slugify(strings.TrimSuffix(a.Filename, path.Ext(a.Filename))); f != "" {
			name += "-" + f
		}
		name += assetExt(a)
		files = append(files, DocFile{Path: tree.Slug + "/assets/" + name, Data: b})
	}
	return tree, files, nil
}

func assetExt(a contract.Asset) string {
	if e := strings.ToLower(path.Ext(a.Filename)); e != "" && len(e) <= 6 && Slugify(e[1:]) == e[1:] {
		return e
	}
	switch a.MIME {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	}
	return ""
}

// derivedPath places a doc by its position: the root is "<slug>/index.md",
// a section "<slug>/<title>/index.md", and a deeper doc "<title>.md" (or
// "<title>/index.md" when it has sub-docs) next to its parent's file.
func derivedPath(slug string, n *contract.Node, parentFile string, depth int, hasKids bool, used map[string]bool) string {
	if depth == 0 {
		return uniquePath(slug+"/index", "", used)
	}
	name := Slugify(n.Title)
	if name == "" {
		name = "doc"
	}
	dir := slug
	if depth > 1 {
		dir = path.Dir(parentFile)
	}
	if depth == 1 || hasKids {
		return uniquePath(dir+"/"+name, "/index", used)
	}
	return uniquePath(dir+"/"+name, "", used)
}

func uniquePath(stem, suffix string, used map[string]bool) string {
	p := stem + suffix + ".md"
	for i := 2; used[p]; i++ {
		p = fmt.Sprintf("%s-%d%s.md", stem, i, suffix)
	}
	return p
}

// docMarkdown is a doc node's file content: its body, under a title heading
// when the body has none.
func docMarkdown(n *contract.Node) string {
	body := strings.TrimSpace(n.BodyMD)
	if !strings.HasPrefix(body, "#") {
		body = strings.TrimSpace("# " + n.Title + "\n\n" + body)
	}
	return body + "\n"
}

// WriteZip writes a doc tree's export as a zip archive.
func WriteZip(w io.Writer, slug string, files []DocFile) error {
	zw := zip.NewWriter(w)
	now := time.Now()
	if _, err := zw.CreateHeader(&zip.FileHeader{Name: slug + "/assets/", Modified: now}); err != nil {
		return err
	}
	for _, f := range files {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.Path, Method: zip.Deflate, Modified: now})
		if err != nil {
			return err
		}
		if _, err := fw.Write(f.Data); err != nil {
			return err
		}
	}
	return zw.Close()
}

// ExportBase is the folder exports are written to: the plugin config's
// export_dir (default ~/Kandev/mindmaps). A map's own export_dir is used
// only when it lies inside that folder.
func (s *Service) ExportBase(ctx context.Context, mapID string) (string, error) {
	cfg := ""
	if s.ExportDir != nil {
		cfg = strings.TrimSpace(s.ExportDir(ctx))
	}
	if cfg == "" {
		cfg = DefaultExportDir
	}
	base, err := expandDir(cfg)
	if err != nil {
		return "", err
	}
	if m, err := s.Store.GetMap(mapID); err == nil && strings.TrimSpace(m.Settings.ExportDir) != "" {
		if d, err := expandDir(m.Settings.ExportDir); err == nil {
			if rel, err := filepath.Rel(base, d); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				base = d
			}
		}
	}
	return base, nil
}

func expandDir(d string) (string, error) {
	d = strings.TrimSpace(d)
	if d == "~" || strings.HasPrefix(d, "~/") || !filepath.IsAbs(d) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", contract.Errf(contract.ErrUnavailable, "no home folder for the export folder: %v", err)
		}
		d = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(d, "~"), "/"))
	}
	return filepath.Clean(d), nil
}

// ExportToDisk writes a doc tree to the export folder and records where.
// Files go under "<base>/<map id>/", since the slug is chosen by whoever
// compiles (guests too): a slug may only replace files of its own map. It
// returns the "<base>/<map id>/<slug>" folder.
func (s *Service) ExportToDisk(ctx context.Context, mapID, docRootID string) (string, error) {
	if !contract.ValidID(contract.PrefixMap, mapID) {
		return "", contract.FieldErr(contract.ErrBadRequest, "map_id", "invalid map id")
	}
	tree, files, err := s.ExportFiles(mapID, docRootID)
	if err != nil {
		return "", err
	}
	base, err := s.ExportBase(ctx, mapID)
	if err != nil {
		return "", err
	}
	base = filepath.Join(base, mapID)
	if err := os.MkdirAll(filepath.Join(base, tree.Slug, "assets"), 0o755); err != nil {
		return "", contract.Errf(contract.ErrUnavailable, "create the export folder: %v", err)
	}
	for _, f := range files {
		full := filepath.Join(base, filepath.FromSlash(f.Path))
		if rel, err := filepath.Rel(base, full); err != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", contract.Errf(contract.ErrUnavailable, "create %s: %v", filepath.Dir(full), err)
		}
		if err := os.WriteFile(full, f.Data, 0o644); err != nil {
			return "", contract.Errf(contract.ErrUnavailable, "write %s: %v", full, err)
		}
	}
	dir := filepath.Join(base, tree.Slug)
	rootID := tree.Index.NodeID
	rec, _ := s.loadDocRecord(rootID)
	if rec.Slug == "" {
		rec.Slug = tree.Slug
	}
	rec.ExportedTo = dir
	s.saveDocRecord(rootID, rec)
	return dir, nil
}
