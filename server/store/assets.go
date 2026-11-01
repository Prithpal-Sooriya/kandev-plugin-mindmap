package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif" // DecodeConfig for asset dimensions
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kandev-plugin-mindmap/server/contract"
)

// errBadBlob is returned for an asset hash that cannot name a blob file.
var errBadBlob = contract.FieldErr(contract.ErrNotFound, "sha256", "invalid asset hash")

// blobPath returns where the bytes with this sha256 live. It refuses
// anything but a lowercase hex hash, and any path that would leave the
// assets folder, since stored rows may predate validation.
func (s *Store) blobPath(sha string) (string, error) {
	if !contract.ValidSHA256(sha) {
		return "", errBadBlob
	}
	root := filepath.Clean(filepath.Join(s.dataDir, "assets"))
	p := filepath.Clean(filepath.Join(root, sha[:2], sha))
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errBadBlob
	}
	return p, nil
}

// BlobPath returns where the bytes with this sha256 live.
func (s *Store) BlobPath(sha string) (string, error) { return s.blobPath(sha) }

// SniffMIME returns the allowed MIME type for data, preferring the declared
// type when the bytes agree with it. ok is false when the type is not in
// contract.AllowedAssetMIME or the bytes contradict an image declaration.
func SniffMIME(data []byte, declared string) (string, bool) {
	declared = strings.TrimSpace(strings.Split(declared, ";")[0])
	sniffed := strings.Split(http.DetectContentType(data), ";")[0]
	switch {
	case strings.HasPrefix(sniffed, "image/") && sniffed != "image/svg+xml":
		_, ok := contract.AssetKindForMIME(sniffed)
		return sniffed, ok
	case declared == "image/svg+xml" || (sniffed == "text/xml" && bytes.Contains(data[:min(len(data), 512)], []byte("<svg"))):
		return "image/svg+xml", true
	case sniffed == "application/pdf":
		return sniffed, true
	case strings.HasPrefix(declared, "image/"):
		return "", false // claims to be a raster image but is not
	}
	if declared != "" {
		_, ok := contract.AssetKindForMIME(declared)
		return declared, ok
	}
	if strings.HasPrefix(sniffed, "text/plain") {
		return "text/plain", true
	}
	return sniffed, false
}

// StoreBlob writes data content-addressed under assets/ and returns an Asset
// (id minted, not yet attached) describing it. mime must already be checked.
func (s *Store) StoreBlob(data []byte, mime string) (contract.Asset, error) {
	if len(data) == 0 {
		return contract.Asset{}, contract.Errf(contract.ErrBadRequest, "empty file")
	}
	if len(data) > contract.MaxAssetBytes {
		return contract.Asset{}, contract.Errf(contract.ErrTooLarge, "asset over %d bytes", contract.MaxAssetBytes)
	}
	kind, ok := contract.AssetKindForMIME(mime)
	if !ok {
		return contract.Asset{}, contract.Errf(contract.ErrUnsupportedMedia, "MIME type %q not allowed", mime)
	}
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	path, err := s.blobPath(sha)
	if err != nil {
		return contract.Asset{}, err
	}
	if _, err := os.Stat(path); err != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return contract.Asset{}, err
		}
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return contract.Asset{}, err
		}
		if err := os.Rename(tmp, path); err != nil {
			return contract.Asset{}, err
		}
	}
	a := contract.Asset{ID: NewID(contract.PrefixAsset), Kind: kind, MIME: mime, Bytes: int64(len(data)), SHA256: sha, CreatedAt: s.now()}
	if kind == contract.AssetImage {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
			a.Width, a.Height = cfg.Width, cfg.Height
		}
	}
	return a, nil
}

// AssetFile returns an attached-or-detached asset of a map and the path of
// its bytes.
func (s *Store) AssetFile(mapID, assetID string) (contract.Asset, string, error) {
	a, err := scanAsset(s.db.QueryRow(`SELECT `+assetCols+` FROM assets WHERE id = ? AND map_id = ?`, assetID, mapID))
	if errors.Is(err, sql.ErrNoRows) {
		return a, "", errNotFound("asset %s not found", assetID)
	}
	if err != nil {
		return a, "", err
	}
	p, err := s.blobPath(a.SHA256)
	if err != nil {
		return a, "", errNotFound("asset %s not found", assetID)
	}
	return a, p, nil
}
