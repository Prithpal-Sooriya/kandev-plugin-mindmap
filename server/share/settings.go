package share

import (
	"strconv"

	"kandev-plugin-mindmap/server/contract"
	"kandev-plugin-mindmap/server/store"
)

// kv keys for plugin-wide share settings. Per-map caps live on MapSettings.
const (
	KeyProvider       = "share.provider"
	KeyMaxConcurrent  = "share.max_concurrent_runs"
	KeyMaxNodes       = "share.max_nodes"
	KeyMaxDepth       = "share.max_depth"
	KeyMaxSubagents   = "share.max_subagents"
	KeyMaxMinutes     = "share.max_minutes"
	KeyRequireApprove = "share.require_guest_approval"
)

// LoadSettings reads plugin-wide share settings. An unset provider is cloudflared.
func LoadSettings(st *store.Store) contract.ShareSettings {
	out := contract.ShareSettings{Provider: contract.ShareCloudflared}
	if st == nil {
		return out
	}
	if v, ok, _ := st.GetKV(KeyProvider); ok && contract.ShareProvider(v).Valid() {
		out.Provider = contract.ShareProvider(v)
	}
	out.MaxConcurrentRuns = kvInt(st, KeyMaxConcurrent)
	out.MaxNodes = kvInt(st, KeyMaxNodes)
	out.MaxDepth = kvInt(st, KeyMaxDepth)
	out.MaxSubagents = kvInt(st, KeyMaxSubagents)
	out.MaxMinutes = kvInt(st, KeyMaxMinutes)
	if v, ok, _ := st.GetKV(KeyRequireApprove); ok && (v == "1" || v == "true") {
		out.RequireGuestRunApproval = true
	}
	return out
}

// SaveSettings writes plugin-wide share settings. An invalid provider is rejected.
func SaveSettings(st *store.Store, in contract.ShareSettings) error {
	if in.Provider == "" {
		in.Provider = contract.ShareCloudflared
	}
	if !in.Provider.Valid() {
		return contract.Errf(contract.ErrBadRequest, "unknown share provider %q", in.Provider)
	}
	for _, n := range []int{in.MaxConcurrentRuns, in.MaxNodes, in.MaxDepth, in.MaxSubagents, in.MaxMinutes} {
		if n < 0 {
			return contract.Errf(contract.ErrBadRequest, "caps must be >= 0")
		}
	}
	if err := st.SetKV(KeyProvider, string(in.Provider)); err != nil {
		return err
	}
	pairs := []struct {
		k string
		n int
	}{
		{KeyMaxConcurrent, in.MaxConcurrentRuns},
		{KeyMaxNodes, in.MaxNodes},
		{KeyMaxDepth, in.MaxDepth},
		{KeyMaxSubagents, in.MaxSubagents},
		{KeyMaxMinutes, in.MaxMinutes},
	}
	for _, p := range pairs {
		if err := st.SetKV(p.k, strconv.Itoa(p.n)); err != nil {
			return err
		}
	}
	v := "0"
	if in.RequireGuestRunApproval {
		v = "1"
	}
	return st.SetKV(KeyRequireApprove, v)
}

func kvInt(st *store.Store, key string) int {
	v, ok, err := st.GetKV(key)
	if err != nil || !ok || v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
