package store

import (
	"testing"

	"kandev-plugin-mindmap/server/contract"
)

func attachOp(mapID string, a contract.Asset) contract.Op {
	return contract.Op{ID: NewID(contract.PrefixOp), MapID: mapID, Type: contract.OpAssetAttach, Payload: &contract.AssetAttachPayload{Asset: a}}
}

func TestAssetAttachRejectsOtherMapsID(t *testing.T) {
	s := openTest(t)
	a1, host1 := newMap(t, s)
	b, host2 := newMap(t, s)
	blob, err := s.StoreBlob([]byte("hello"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	asset := blob
	asset.NodeID, asset.Caption = a1.RootID, "A's caption"
	if _, err := s.Submit(a1.ID, []contract.Op{attachOp(a1.ID, asset)}, SubmitOptions{Actor: contract.Actor{ParticipantID: host1.ID}}); err != nil {
		t.Fatal(err)
	}

	stolen := asset
	stolen.NodeID, stolen.Caption = b.RootID, "B's caption"
	if _, err := s.Submit(b.ID, []contract.Op{attachOp(b.ID, stolen)}, SubmitOptions{Actor: contract.Actor{ParticipantID: host2.ID}}); err == nil {
		t.Fatal("attach with another map's asset id was accepted")
	}
	got, _, err := s.AssetFile(a1.ID, asset.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NodeID != a1.RootID || got.Caption != "A's caption" {
		t.Fatalf("map A's asset was rewritten: %+v", got)
	}
	if _, _, err := s.AssetFile(b.ID, asset.ID); code(err) != contract.ErrNotFound {
		t.Fatalf("map B sees the asset: %v", err)
	}
	snap, err := s.Snapshot(b.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range snap.Nodes {
		if len(n.Assets) > 0 {
			t.Fatalf("map B kept the rejected attach in memory: %+v", n.Assets)
		}
	}

	// Detach and reattach on the same map updates the row (caption included).
	detach := contract.Op{ID: NewID(contract.PrefixOp), MapID: a1.ID, Type: contract.OpAssetDetach,
		Payload: &contract.AssetDetachPayload{ID: asset.ID, NodeID: a1.RootID}}
	if _, err := s.Submit(a1.ID, []contract.Op{detach}, SubmitOptions{Actor: contract.Actor{ParticipantID: host1.ID}}); err != nil {
		t.Fatal(err)
	}
	again := asset
	again.Caption = "New caption"
	if _, err := s.Submit(a1.ID, []contract.Op{attachOp(a1.ID, again)}, SubmitOptions{Actor: contract.Actor{ParticipantID: host1.ID}}); err != nil {
		t.Fatalf("same-map reattach: %v", err)
	}
	if got, _, err := s.AssetFile(a1.ID, asset.ID); err != nil || got.Caption != "New caption" {
		t.Fatalf("same-map reattach: %+v %v", got, err)
	}
}
