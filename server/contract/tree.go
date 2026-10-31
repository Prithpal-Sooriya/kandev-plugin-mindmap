package contract

import "fmt"

// ParentIndex maps every live (non-deleted) node ID to its parent ID. The
// root maps to "". It is the only map state the tree checks need, so the
// backend, the UI and tests can all build one cheaply.
type ParentIndex map[string]string

// BuildParentIndex indexes the live nodes of one map.
func BuildParentIndex(nodes []Node) ParentIndex {
	idx := make(ParentIndex, len(nodes))
	for _, n := range nodes {
		if !n.Deleted {
			idx[n.ID] = n.ParentID
		}
	}
	return idx
}

// IsAncestor reports whether anc is a strict ancestor of id. It stops after
// len(idx) steps so a corrupt (cyclic) index cannot loop forever.
func (idx ParentIndex) IsAncestor(anc, id string) bool {
	cur := id
	for range len(idx) + 1 {
		p, ok := idx[cur]
		if !ok || p == "" {
			return false
		}
		if p == anc {
			return true
		}
		cur = p
	}
	return false
}

// Depth returns the number of edges from the root to id, or -1 if id is not
// in the index or the index is cyclic.
func (idx ParentIndex) Depth(id string) int {
	if _, ok := idx[id]; !ok {
		return -1
	}
	d, cur := 0, id
	for range len(idx) + 1 {
		p := idx[cur]
		if p == "" {
			return d
		}
		if _, ok := idx[p]; !ok {
			return -1
		}
		d++
		cur = p
	}
	return -1
}

// Ancestors returns the path from the root down to id's parent.
func (idx ParentIndex) Ancestors(id string) []string {
	var out []string
	cur := id
	for range len(idx) + 1 {
		p, ok := idx[cur]
		if !ok || p == "" {
			break
		}
		out = append(out, p)
		cur = p
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// CheckMove validates moving id under newParent: both must exist, the root
// cannot move, and newParent cannot be id or one of id's descendants.
func (idx ParentIndex) CheckMove(rootID, id, newParent string) error {
	if id == rootID {
		return FieldErr(ErrRootImmutable, "id", "the root node cannot be moved")
	}
	if _, ok := idx[id]; !ok {
		return FieldErr(ErrNotFound, "id", "node %s not found", id)
	}
	if _, ok := idx[newParent]; !ok {
		return FieldErr(ErrNotFound, "parent_id", "node %s not found", newParent)
	}
	if id == newParent || idx.IsAncestor(id, newParent) {
		return FieldErr(ErrCycle, "parent_id", "moving %s under %s would create a cycle", id, newParent)
	}
	return nil
}

// CheckDelete validates deleting id.
func (idx ParentIndex) CheckDelete(rootID, id string) error {
	if id == rootID {
		return FieldErr(ErrRootImmutable, "id", "the root node cannot be deleted")
	}
	if _, ok := idx[id]; !ok {
		return FieldErr(ErrNotFound, "id", "node %s not found", id)
	}
	return nil
}

// CheckMerge validates merging ids into intoID. intoID may not be the root's
// victim, nor live inside any merged node (its parent would be tombstoned).
func (idx ParentIndex) CheckMerge(rootID, intoID string, ids []string) error {
	if _, ok := idx[intoID]; !ok {
		return FieldErr(ErrNotFound, "into_id", "node %s not found", intoID)
	}
	for i, id := range ids {
		f := fmt.Sprintf("ids[%d]", i)
		if id == rootID {
			return FieldErr(ErrRootImmutable, f, "the root node cannot be merged away")
		}
		if _, ok := idx[id]; !ok {
			return FieldErr(ErrNotFound, f, "node %s not found", id)
		}
		if idx.IsAncestor(id, intoID) {
			return FieldErr(ErrCycle, "into_id", "%s is inside %s, which is being merged", intoID, id)
		}
	}
	return nil
}

// CheckCreate validates the parent of a new node. A node with no parent is
// only legal as the first node of an empty map.
func (idx ParentIndex) CheckCreate(id, parentID string) error {
	if _, exists := idx[id]; exists {
		return FieldErr(ErrConflict, "node.id", "node %s already exists", id)
	}
	if parentID == "" {
		if len(idx) != 0 {
			return FieldErr(ErrInvalidOp, "node.parent_id", "only the first node of a map may be a root")
		}
		return nil
	}
	if _, ok := idx[parentID]; !ok {
		return FieldErr(ErrNotFound, "node.parent_id", "parent %s not found", parentID)
	}
	return nil
}

// ValidateTree checks that the live nodes of one map form a single tree: one
// root, every parent exists, no cycles, all map_ids equal.
func ValidateTree(nodes []Node) error {
	idx := BuildParentIndex(nodes)
	roots := 0
	var mapID string
	for _, n := range nodes {
		if n.Deleted {
			continue
		}
		if mapID == "" {
			mapID = n.MapID
		} else if n.MapID != mapID {
			return Errf(ErrInvalidOp, "node %s belongs to map %s, expected %s", n.ID, n.MapID, mapID)
		}
		if n.ParentID == "" {
			roots++
			continue
		}
		if _, ok := idx[n.ParentID]; !ok {
			return Errf(ErrNotFound, "node %s has missing or deleted parent %s", n.ID, n.ParentID)
		}
		if idx.Depth(n.ID) < 0 {
			return Errf(ErrCycle, "node %s is on a cycle", n.ID)
		}
	}
	if len(idx) > 0 && roots != 1 {
		return Errf(ErrInvalidOp, "expected exactly one root, found %d", roots)
	}
	return nil
}
