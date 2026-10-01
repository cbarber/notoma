package sync

import (
	"testing"
)

const (
	idSmall = "11111111-aaaa-4000-8000-000000000001"
	idMid   = "22222222-bbbb-4000-8000-000000000002"
	idLarge = "33333333-cccc-4000-8000-000000000003"
)

func TestPathAllocator_NoCollision(t *testing.T) {
	a := NewPathAllocator(nil)
	if got := a.Allocate(idLarge, "", "Engineering", ".md"); got != "Engineering.md" {
		t.Errorf("Allocate() = %q, want Engineering.md", got)
	}
	if got := a.Allocate(idSmall, "Wiki", "Engineering", ".md"); got != "Wiki/Engineering.md" {
		t.Errorf("same name in another folder = %q, want Wiki/Engineering.md", got)
	}
	if got := a.Allocate(idLarge, "", "Ignored", ".md"); got != "Engineering.md" {
		t.Errorf("repeat Allocate() = %q, want the first allocation", got)
	}
}

func TestPathAllocator_FirstClaimWinsWithinRun(t *testing.T) {
	a := NewPathAllocator(nil)
	if got := a.Allocate(idLarge, "", "Engineering", ".md"); got != "Engineering.md" {
		t.Errorf("first claim = %q, want Engineering.md", got)
	}
	if got := a.Allocate(idSmall, "", "Engineering", ".md"); got != "Engineering (00000001).md" {
		t.Errorf("second claim = %q, want suffix from the last 8 hex chars", got)
	}
}

func TestPathAllocator_StateOwnerKeepsPath(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{ID: idLarge, Type: ResourceTypePage, LocalPath: "Engineering.md"})

	a := NewPathAllocator(state)
	if got := a.Allocate(idSmall, "", "Engineering", ".md"); got != "Engineering (00000001).md" {
		t.Errorf("Allocate(not owner) = %q, want suffixed", got)
	}
	if got := a.Allocate(idLarge, "", "Engineering", ".md"); got != "Engineering.md" {
		t.Errorf("Allocate(owner) = %q, want Engineering.md", got)
	}
}

func TestPathAllocator_StateOwnerOfEntryAndFolder(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{
		ID: idLarge, Type: ResourceTypeDatabase, LocalPath: "Action Items",
		Entries: map[string]EntryState{idMid: {PageID: idMid, LocalFile: "Task.md"}},
	})

	a := NewPathAllocator(state)
	if got := a.Allocate(idSmall, "", "Action Items", ""); got != "Action Items (00000001)" {
		t.Errorf("folder = %q, want suffixed folder", got)
	}
	if got := a.Allocate(idSmall+"x", "Action Items", "Task", ".md"); got != "Action Items/Task (0000001x).md" {
		t.Errorf("entry = %q, want suffixed entry", got)
	}
}

// Older states can record two IDs at one path; the smaller must win every
// time, whatever the map iteration order.
func TestPathAllocator_SeedingIsDeterministic(t *testing.T) {
	for i := 0; i < 50; i++ {
		state := NewSyncState()
		state.SetResource(ResourceState{ID: idLarge, Type: ResourceTypePage, LocalPath: "Engineering.md"})
		state.SetResource(ResourceState{ID: idSmall, Type: ResourceTypePage, LocalPath: "Engineering.md"})

		a := NewPathAllocator(state)
		if got := a.Allocate(idLarge, "", "Engineering", ".md"); got != "Engineering (00000003).md" {
			t.Fatalf("run %d: larger ID got %q, want suffixed", i, got)
		}
		if got := a.Allocate(idSmall, "", "Engineering", ".md"); got != "Engineering.md" {
			t.Fatalf("run %d: smaller ID got %q, want Engineering.md", i, got)
		}
	}
}

func TestPathAllocator_KeepsOwnSuffixedPath(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{ID: idMid, Type: ResourceTypePage, LocalPath: "X (00000002).md"})

	a := NewPathAllocator(state)
	if got := a.Allocate(idMid, "", "X", ".md"); got != "X (00000002).md" {
		t.Errorf("Allocate() = %q, want its state path kept though X.md is free", got)
	}
	if got := a.Allocate(idSmall, "", "X", ".md"); got != "X.md" {
		t.Errorf("Allocate(other) = %q, want the free bare path", got)
	}
}

func TestPathAllocator_OwnPathFollowsTitleCase(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{ID: idMid, Type: ResourceTypePage, LocalPath: "notes.md"})

	a := NewPathAllocator(state)
	if got := a.Allocate(idMid, "", "Notes", ".md"); got != "Notes.md" {
		t.Errorf("Allocate() = %q, want the new case", got)
	}
}

func TestPathAllocator_OwnPathNotKeptForNewTitle(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{ID: idMid, Type: ResourceTypePage, LocalPath: "Old (00000002).md"})

	a := NewPathAllocator(state)
	if got := a.Allocate(idMid, "", "New", ".md"); got != "New.md" {
		t.Errorf("Allocate() after rename = %q, want New.md", got)
	}
}

func TestPathAllocator_CaseOnlyCollision(t *testing.T) {
	a := NewPathAllocator(nil)
	a.Allocate(idSmall, "", "engineering", ".md")
	if got := a.Allocate(idLarge, "", "Engineering", ".md"); got != "Engineering (00000003).md" {
		t.Errorf("case-only collision = %q, want suffixed", got)
	}
}

func TestPathAllocator_ThreeWayCollision(t *testing.T) {
	a := NewPathAllocator(nil)
	got := []string{
		a.Allocate(idMid, "", "Notes", ".md"),
		a.Allocate(idLarge, "", "Notes", ".md"),
		a.Allocate(idSmall, "", "Notes", ".md"),
	}
	want := []string{"Notes.md", "Notes (00000003).md", "Notes (00000001).md"}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("allocation %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPathAllocator_SharedSuffixFallsBackToFullID(t *testing.T) {
	a := NewPathAllocator(nil)
	first := "aaaaaaaa-0000-4000-8000-000012345678"
	second := "bbbbbbbb-0000-4000-8000-000012345678"
	third := "cccccccc-0000-4000-8000-000012345678"
	a.Allocate(first, "", "Action Items", "")
	if got := a.Allocate(second, "", "Action Items", ""); got != "Action Items (12345678)" {
		t.Errorf("second = %q, want short suffix", got)
	}
	if got := a.Allocate(third, "", "Action Items", ""); got != "Action Items (cccccccc000040008000000012345678)" {
		t.Errorf("third = %q, want full-ID suffix", got)
	}
}

// Recording a run's paths in state and allocating again in another order
// must give every resource the same path.
func TestPathAllocator_StableAcrossRuns(t *testing.T) {
	first := NewPathAllocator(nil)
	order := []string{idLarge, idMid, idSmall}
	paths := make(map[string]string)
	for _, id := range order {
		paths[id] = first.Allocate(id, "", "Notes", ".md")
	}

	state := NewSyncState()
	for _, id := range order {
		state.SetResource(ResourceState{ID: id, Type: ResourceTypePage, LocalPath: paths[id]})
	}

	second := NewPathAllocator(state)
	for _, id := range []string{idSmall, idLarge, idMid} {
		if got := second.Allocate(id, "", "Notes", ".md"); got != paths[id] {
			t.Errorf("run 2 Allocate(%s) = %q, want %q", id, got, paths[id])
		}
	}
}

func TestRelocateFolder(t *testing.T) {
	state := NewSyncState()
	state.SetResource(ResourceState{ID: "db", Type: ResourceTypeDatabase, LocalPath: "Tasks"})
	state.SetResource(ResourceState{ID: "child", Type: ResourceTypePage, LocalPath: "Tasks/Child.md"})
	state.SetResource(ResourceState{ID: "other", Type: ResourceTypePage, LocalPath: "Tasks Archive.md"})

	state.RelocateFolder("Tasks", "Tasks (abc)")

	want := map[string]string{"db": "Tasks (abc)", "child": "Tasks (abc)/Child.md", "other": "Tasks Archive.md"}
	for id, w := range want {
		if got := state.GetResource(id).LocalPath; got != w {
			t.Errorf("%s path = %q, want %q", id, got, w)
		}
	}
}
