package sync

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func newTestCheckpointer(t *testing.T, ctx context.Context, every int, dryRun bool, beforeSave func()) (*Checkpointer, *SyncState, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	state := NewSyncState()
	cp := NewCheckpointer(ctx, path, state, every, dryRun, beforeSave, slog.New(slog.NewTextHandler(io.Discard, nil)))
	state.OnChange(cp.Record)
	return cp, state, path
}

func addPages(state *SyncState, ids ...string) {
	for _, id := range ids {
		state.SetResource(ResourceState{ID: id, Type: ResourceTypePage, Title: id})
	}
}

func TestCheckpointer_SavesEveryN(t *testing.T) {
	_, state, path := newTestCheckpointer(t, context.Background(), 3, false, nil)

	addPages(state, "a", "b")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("state should not be saved before the interval is reached")
	}

	addPages(state, "c")
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("loading checkpoint: %v", err)
	}
	if loaded.ResourceCount() != 3 {
		t.Errorf("expected 3 resources in checkpoint, got %d", loaded.ResourceCount())
	}

	// The counter resets after a save, so the next checkpoint is 3 more away.
	addPages(state, "d", "e")
	loaded, _ = LoadState(path)
	if loaded.ResourceCount() != 3 {
		t.Errorf("expected checkpoint to still hold 3 resources, got %d", loaded.ResourceCount())
	}
	addPages(state, "f")
	loaded, _ = LoadState(path)
	if loaded.ResourceCount() != 6 {
		t.Errorf("expected 6 resources in second checkpoint, got %d", loaded.ResourceCount())
	}
}

func TestCheckpointer_CountsDatabaseEntries(t *testing.T) {
	_, state, path := newTestCheckpointer(t, context.Background(), 2, false, nil)

	// Registering the database counts as one change, the entry as another.
	state.SetResource(ResourceState{ID: "db", Type: ResourceTypeDatabase, Entries: map[string]EntryState{}})
	if err := state.SetEntry("db", EntryState{PageID: "e1"}); err != nil {
		t.Fatalf("SetEntry: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("loading checkpoint: %v", err)
	}
	if loaded.GetEntry("db", "e1") == nil {
		t.Error("expected entry e1 in checkpoint")
	}
}

func TestCheckpointer_CancelledContextSaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, state, path := newTestCheckpointer(t, ctx, 100, false, nil)

	addPages(state, "a")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("state should not be saved before cancellation")
	}

	cancel()
	addPages(state, "b")
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("loading checkpoint: %v", err)
	}
	if loaded.ResourceCount() != 2 {
		t.Errorf("expected 2 resources saved after cancellation, got %d", loaded.ResourceCount())
	}
}

func TestCheckpointer_DryRunNeverWrites(t *testing.T) {
	called := false
	cp, state, path := newTestCheckpointer(t, context.Background(), 1, true, func() { called = true })

	addPages(state, "a", "b", "c")
	if err := cp.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("dry-run must not write the state file")
	}
	if called {
		t.Error("dry-run must not flush attachments")
	}
}

func TestCheckpointer_BeforeSaveRunsFirst(t *testing.T) {
	var stateExisted *bool
	var path string
	beforeSave := func() {
		_, err := os.Stat(path)
		existed := err == nil
		stateExisted = &existed
	}
	_, state, p := newTestCheckpointer(t, context.Background(), 1, false, beforeSave)
	path = p

	addPages(state, "a")
	if stateExisted == nil {
		t.Fatal("beforeSave was not called")
	}
	if *stateExisted {
		t.Error("beforeSave must run before the state file is written")
	}
}

func TestSaveState_FailedWriteKeepsPrevious(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")

	state := NewSyncState()
	state.SetResource(ResourceState{ID: "v1", Type: ResourceTypePage})
	if err := SaveState(path, state); err != nil {
		t.Fatalf("saving initial state: %v", err)
	}

	// A directory at the temp path makes the temp-file write fail.
	if err := os.Mkdir(path+".tmp", 0o755); err != nil {
		t.Fatalf("creating blocking dir: %v", err)
	}
	state.SetResource(ResourceState{ID: "v2", Type: ResourceTypePage})
	if err := SaveState(path, state); err == nil {
		t.Fatal("expected save to fail")
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("previous state should still load: %v", err)
	}
	if loaded.GetResource("v1") == nil || loaded.GetResource("v2") != nil {
		t.Errorf("expected previous state with only v1, got %v", loaded.Resources)
	}
}
