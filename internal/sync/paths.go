package sync

import (
	"path"
	"strings"
)

// PathAllocator assigns each Notion resource a vault path that no other
// resource holds, so same-titled pages and databases never overwrite each
// other. Paths compare case-insensitively, matching the macOS default.
type PathAllocator struct {
	// stateOwners maps a folded path to the ID that held it in the loaded
	// state; that owner keeps it.
	stateOwners map[string]string
	// statePaths maps an ID to its path in the loaded state.
	statePaths map[string]string
	// claims maps a folded path to the ID holding it in this run.
	claims map[string]string
	// paths maps an ID to the path it holds in this run.
	paths map[string]string
}

// NewPathAllocator seeds ownership from the paths recorded in state.
func NewPathAllocator(state *SyncState) *PathAllocator {
	a := &PathAllocator{
		stateOwners: make(map[string]string),
		statePaths:  make(map[string]string),
		claims:      make(map[string]string),
		paths:       make(map[string]string),
	}
	if state == nil {
		return a
	}
	for id, res := range state.Resources {
		if res.LocalPath != "" {
			a.seed(id, res.LocalPath)
		}
		for entryID, entry := range res.Entries {
			if entry.LocalFile != "" {
				a.seed(entryID, joinPath(res.LocalPath, entry.LocalFile))
			}
		}
	}
	return a
}

// seed records that id held p in state. Older states can record two IDs
// at one path; the smaller ID keeps it so the outcome doesn't depend on
// map iteration order.
func (a *PathAllocator) seed(id, p string) {
	a.statePaths[id] = p
	key := foldPath(p)
	if owner, ok := a.stateOwners[key]; ok && compactID(owner) < compactID(id) {
		return
	}
	a.stateOwners[key] = id
}

// Allocate returns the path for id: dir/name+ext when free, otherwise
// dir/name (1234abcd)+ext from the last 8 hex chars of the ID, or the
// full ID if that is taken too. A path owned in state stays with its
// owner; otherwise the first claim in a run wins. An ID keeps its own
// state path while that is still one of its candidates, so a suffixed
// path doesn't move once the bare one frees up. Repeated calls for the
// same ID return the same path.
func (a *PathAllocator) Allocate(id, dir, name, ext string) string {
	if p, ok := a.paths[id]; ok {
		return p
	}

	compact := compactID(id)
	short := compact
	if len(short) > 8 {
		short = short[len(short)-8:]
	}
	candidates := []string{
		joinPath(dir, name+ext),
		joinPath(dir, name+" ("+short+")"+ext),
		joinPath(dir, name+" ("+compact+")"+ext),
	}

	if own, ok := a.statePaths[id]; ok {
		for _, c := range candidates {
			if foldPath(c) == foldPath(own) && !a.takenByOther(c, id) {
				return a.claim(id, c)
			}
		}
	}
	for _, c := range candidates[:2] {
		if !a.takenByOther(c, id) {
			return a.claim(id, c)
		}
	}
	// No other ID can hold a path containing this ID in full.
	return a.claim(id, candidates[2])
}

func (a *PathAllocator) claim(id, p string) string {
	a.claims[foldPath(p)] = id
	a.paths[id] = p
	return p
}

func (a *PathAllocator) takenByOther(p, id string) bool {
	key := foldPath(p)
	if owner := a.stateOwners[key]; owner != "" && owner != id {
		return true
	}
	holder := a.claims[key]
	return holder != "" && holder != id
}

func joinPath(dir, file string) string {
	if dir == "" {
		return file
	}
	return path.Join(dir, file)
}

func foldPath(p string) string {
	return strings.ToLower(p)
}

func compactID(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

// RelocateFolder updates every recorded path at or under folder from to
// sit under to instead.
func (s *SyncState) RelocateFolder(from, to string) {
	for id, res := range s.Resources {
		switch {
		case res.LocalPath == from:
			res.LocalPath = to
		case strings.HasPrefix(res.LocalPath, from+"/"):
			res.LocalPath = to + strings.TrimPrefix(res.LocalPath, from)
		default:
			continue
		}
		s.Resources[id] = res
	}
}
