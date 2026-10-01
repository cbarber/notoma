package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jomei/notionapi"
	"github.com/natikgadzhi/notion-based/internal/notion"
	"github.com/natikgadzhi/notion-based/internal/sync"
	"github.com/natikgadzhi/notion-based/internal/transform"
	"github.com/natikgadzhi/notion-based/internal/writer"
)

const toggleID = "00000000-0000-0000-0000-0000000000aa"

// newRemovalTree syncs root → child → grandchild plus fillers leaf pages
// under root, so that removing one page stays under the 20% threshold.
func newRemovalTree(t *testing.T, fillers int) (*fakeNotion, *sync.SyncState, string) {
	t.Helper()
	fake := newFakeNotion()
	fake.add(rootID, "Root", "root", t0, childID)
	fake.add(childID, "Child", "child", t0, grandchildID)
	fake.add(grandchildID, "Grandchild", "grandchild", t0)
	for i := range fillers {
		id := fmt.Sprintf("00000000-0000-0000-0000-1000000000%02d", i)
		fake.add(id, fmt.Sprintf("Filler %d", i), "filler", t0)
		fake.pages[rootID].children = append(fake.pages[rootID].children, id)
	}
	state := sync.NewSyncState()
	vault := t.TempDir()
	runFakeSync(t, fake, state, vault, rootID)
	fake.resetRequests()
	return fake, state, vault
}

// syncAndRemove runs a sync and the removal step after it, as runSync does.
func syncAndRemove(t *testing.T, sc *syncContext, fake *fakeNotion) {
	t.Helper()
	syncFakeRoot(t, sc, fake, rootID)
	removeIfComplete(sc, nil)
}

func TestRemove_DeletedCachedChildRemovesOnlyIt(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	// The child is unchanged, so only its cached children say the
	// grandchild exists; Notion now answers 404 for it.
	delete(fake.pages, grandchildID)

	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if exists(vault, "Grandchild.md") || state.GetResource(grandchildID) != nil {
		t.Error("deleted grandchild was not removed")
	}
	if !exists(vault, "Child.md") || !exists(vault, "Root.md") || state.ResourceCount() != 8 {
		t.Errorf("other pages were removed: %d resources left", state.ResourceCount())
	}
}

func TestRemove_TrashedChildUnderUnchangedParent(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	// Notion still serves a trashed page, with a newer edit time.
	fake.pages[grandchildID].archived = true
	fake.pages[grandchildID].edited = t0.Add(time.Hour)

	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if exists(vault, "Grandchild.md") || state.GetResource(grandchildID) != nil {
		t.Error("trashed grandchild was not removed")
	}
	if fake.requestsFor(blocksPath(grandchildID)) != 0 {
		t.Error("trashed grandchild's blocks were fetched")
	}
}

func TestRemove_TrashedPageFromSearchIsConfirmed(t *testing.T) {
	for _, trashed := range []bool{true, false} {
		t.Run(fmt.Sprintf("trashed=%v", trashed), func(t *testing.T) {
			fake, state, vault := newRemovalTree(t, 6)
			// Search says trashed; the page itself has the last word,
			// since search lags behind a restore.
			fake.pages[grandchildID].archived = trashed
			sc := fakeSyncContext(t, fake, state, vault, rootID)
			sc.archived = map[string]bool{grandchildID: true}

			syncAndRemove(t, sc, fake)

			if fake.requestsFor("GET /v1/pages/"+grandchildID) != 1 {
				t.Errorf("requests = %v, want the page fetched to confirm", fake.requests)
			}
			if removed := state.GetResource(grandchildID) == nil; removed != trashed {
				t.Errorf("removed = %v, want %v", removed, trashed)
			}
		})
	}
}

func TestRemove_IncompleteWalkDeletesNothing(t *testing.T) {
	const dbID = "00000000-0000-0000-0000-0000000000e5"
	const rowID = "00000000-0000-0000-0000-0000000000f6"
	tests := []struct {
		name  string
		setup func(fake *fakeNotion, sc *syncContext)
	}{
		{"partial scan", func(fake *fakeNotion, _ *syncContext) {
			fake.pages[childID].toggle = toggleID
			fake.failBlocks[toggleID] = true
		}},
		{"child fetch fails", func(fake *fakeNotion, _ *syncContext) {
			fake.failBlocks[childID] = true
		}},
		{"GetPage fallback fails", func(fake *fakeNotion, sc *syncContext) {
			delete(sc.lastEdited, childID)
			fake.failPages[childID] = true
		}},
		{"child database fails", func(fake *fakeNotion, _ *syncContext) {
			fake.pages[rootID].databases = []string{dbID}
			fake.pages[rootID].edited = t0.Add(time.Hour)
			fake.dbFails = true
		}},
		{"entry blocks fail", func(fake *fakeNotion, _ *syncContext) {
			fake.pages[rootID].databases = []string{dbID}
			fake.pages[rootID].edited = t0.Add(time.Hour)
			fake.add(rowID, "Row", "row", t0)
			fake.rows = []string{rowID}
			fake.failBlocks[rowID] = true
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake, state, vault := newRemovalTree(t, 6)
			// The child no longer lists the grandchild, which is gone.
			fake.pages[childID].children = nil
			fake.pages[childID].edited = t0.Add(time.Hour)
			delete(fake.pages, grandchildID)
			sc := fakeSyncContext(t, fake, state, vault, rootID)
			tt.setup(fake, sc)

			syncAndRemove(t, sc, fake)

			if sc.incomplete == "" {
				t.Error("walk not marked incomplete")
			}
			if !exists(vault, "Grandchild.md") || state.GetResource(grandchildID) == nil {
				t.Error("removed after an incomplete walk")
			}
		})
	}
}

func TestRemove_FailedRootDeletesNothing(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	delete(fake.pages, grandchildID)
	sc := fakeSyncContext(t, fake, state, vault, rootID)
	syncFakeRoot(t, sc, fake, rootID)

	removeIfComplete(sc, errors.New("another root failed"))

	if !exists(vault, "Grandchild.md") || state.GetResource(grandchildID) == nil {
		t.Error("removed after a failed root")
	}
}

func TestRemove_DryRunOnlyLogs(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	delete(fake.pages, grandchildID)
	sc := fakeSyncContext(t, fake, state, vault, rootID)
	var logs bytes.Buffer
	sc.logger = slog.New(slog.NewTextHandler(&logs, nil))
	sc.writer = writer.New(vault, "_attachments", true, sc.logger)
	sc.dryRun = true

	syncAndRemove(t, sc, fake)

	if !exists(vault, "Grandchild.md") || state.GetResource(grandchildID) == nil {
		t.Error("dry run removed the page")
	}
	if !strings.Contains(logs.String(), `msg="would remove page" title=Grandchild`) {
		t.Errorf("dry run did not log the removal: %s", logs.String())
	}
}

func TestRemove_OverThresholdDeletesNothing(t *testing.T) {
	// Removing child and grandchild is 2 of 9 tracked, over 20%.
	fake, state, vault := newRemovalTree(t, 6)
	fake.pages[rootID].children = fake.pages[rootID].children[1:]
	fake.pages[rootID].edited = t0.Add(time.Hour)

	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if !exists(vault, "Child.md") || state.GetResource(childID) == nil || state.GetResource(grandchildID) == nil {
		t.Error("removed more than the threshold allows")
	}
}

func TestRemove_KeepsFileWithOtherNotionID(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	delete(fake.pages, grandchildID)
	foreign := transform.PageFrontmatter("someone-else") + "\nmine\n"
	writeVaultFile(t, vault, "Grandchild.md", foreign)

	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if got := readVaultFile(t, vault, "Grandchild.md"); got != foreign {
		t.Errorf("file owned by another page changed: %q", got)
	}
	if state.GetResource(grandchildID) != nil {
		t.Error("removed page still tracked")
	}
}

func TestRemove_FreedPathClaimableNextRun(t *testing.T) {
	const newID = "00000000-0000-0000-0000-0000000000d4"
	fake, state, vault := newRemovalTree(t, 6)
	delete(fake.pages, grandchildID)
	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	fake.add(newID, "Grandchild", "replacement", t0)
	fake.pages[childID].children = []string{newID}
	fake.pages[childID].edited = t0.Add(time.Hour)
	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if p := state.GetResource(newID).LocalPath; p != "Grandchild.md" {
		t.Errorf("new page path = %q, want the freed bare name", p)
	}
}

func TestRemove_DatabaseRows(t *testing.T) {
	const dbID = "00000000-0000-0000-0000-0000000000e5"
	fake := newFakeNotion()
	for i := range 10 {
		id := fmt.Sprintf("00000000-0000-0000-0000-2000000000%02d", i)
		fake.add(id, fmt.Sprintf("Row %d", i), "row", t0)
		fake.rows = append(fake.rows, id)
	}
	state := sync.NewSyncState()
	vault := t.TempDir()
	syncDB := func() {
		sc := fakeSyncContext(t, fake, state, vault, dbID)
		if err := syncDatabase(sc, &notion.Resource{ID: dbID, Type: notion.ResourceTypeDatabase, Title: "Tasks"}, ""); err != nil {
			t.Fatalf("syncDatabase() error = %v", err)
		}
		removeIfComplete(sc, nil)
	}
	syncDB()

	deleted, trashed := fake.rows[3], fake.rows[4]
	fake.rows = append(fake.rows[:3:3], fake.rows[4:]...)
	fake.pages[trashed].archived = true
	syncDB()

	for _, f := range []string{"Tasks/Row 3.md", "Tasks/Row 4.md"} {
		if exists(vault, f) {
			t.Errorf("%s was not removed", f)
		}
	}
	if state.GetEntry(dbID, deleted) != nil || state.GetEntry(dbID, trashed) != nil {
		t.Error("removed rows still tracked")
	}
	if !exists(vault, "Tasks/Row 5.md") || state.EntryCount() != 8 {
		t.Errorf("other rows were removed: %d left", state.EntryCount())
	}
}

func TestRemove_WholeDatabase(t *testing.T) {
	const dbID = "00000000-0000-0000-0000-0000000000e5"
	fake, state, vault := newRemovalTree(t, 12)
	for i := range 2 {
		id := fmt.Sprintf("00000000-0000-0000-0000-2000000000%02d", i)
		fake.add(id, fmt.Sprintf("Row %d", i), "row", t0)
		fake.rows = append(fake.rows, id)
	}
	fake.pages[rootID].databases = []string{dbID}
	fake.pages[rootID].edited = t0.Add(time.Hour)
	runFakeSync(t, fake, state, vault, rootID)
	if !exists(vault, "Tasks/Row 0.md") || !exists(vault, "Tasks.base") {
		t.Fatal("database was not synced")
	}

	// Root is unchanged; its cached children still list the database.
	fake.dbGone = true
	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if exists(vault, "Tasks") || exists(vault, "Tasks.base") {
		t.Error("database folder or base file left behind")
	}
	if state.GetResource(dbID) != nil {
		t.Error("removed database still tracked")
	}
}

func TestRemove_CapCountsStateBeforeTheWalk(t *testing.T) {
	// 2 of the 9 tracked are removed while 20 new pages arrive; against
	// the 31 tracked after the walk that would pass the cap.
	fake, state, vault := newRemovalTree(t, 6)
	fake.pages[rootID].children = fake.pages[rootID].children[1:]
	for i := range 20 {
		id := fmt.Sprintf("00000000-0000-0000-0000-3000000000%02d", i)
		fake.add(id, fmt.Sprintf("New %d", i), "new", t0)
		fake.pages[rootID].children = append(fake.pages[rootID].children, id)
	}
	fake.pages[rootID].edited = t0.Add(time.Hour)

	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)

	if state.GetResource(childID) == nil || !exists(vault, "Child.md") {
		t.Error("removed over the cap of what was tracked before the run")
	}
}

func TestRemove_MissingRootBlocksDeletionUnlessPruning(t *testing.T) {
	const otherRoot = "00000000-0000-0000-0000-0000000000ff"
	for _, pruning := range []bool{false, true} {
		t.Run(fmt.Sprintf("prune=%v", pruning), func(t *testing.T) {
			fake, state, vault := newRemovalTree(t, 6)
			state.Roots = []string{otherRoot, rootID}
			delete(fake.pages, grandchildID)
			sc := fakeSyncContext(t, fake, state, vault, rootID)
			sc.prune = pruning

			syncAndRemove(t, sc, fake)

			if removed := state.GetResource(grandchildID) == nil; removed != pruning {
				t.Errorf("removed = %v, want %v", removed, pruning)
			}
			wantRoots := []string{otherRoot, rootID}
			if pruning {
				wantRoots = []string{rootID}
			}
			if fmt.Sprint(state.Roots) != fmt.Sprint(wantRoots) {
				t.Errorf("roots = %v, want %v", state.Roots, wantRoots)
			}
		})
	}
}

func TestRemove_CompleteRunRecordsRoots(t *testing.T) {
	fake, state, vault := newRemovalTree(t, 6)
	syncAndRemove(t, fakeSyncContext(t, fake, state, vault, rootID), fake)
	if fmt.Sprint(state.Roots) != fmt.Sprint([]string{rootID}) {
		t.Errorf("roots = %v, want this run's root", state.Roots)
	}
}

func TestScanChildren_RetryableErrorMarksIncomplete(t *testing.T) {
	sc, _ := newPathsTestContext(t, sync.NewSyncState())
	fetcher := newCountingFetcher(map[string][]notionapi.Block{})
	fetcher.errs["toggle"] = &notionapi.Error{Status: 500}
	blocks := []notionapi.Block{&notionapi.ToggleBlock{BasicBlock: basic("toggle", notionapi.BlockTypeToggle, true)}}

	_, _, children := scanChildren(sc, &notion.Resource{ID: "page"}, blocks, fetcher)

	if children != nil || sc.incomplete == "" {
		t.Errorf("children = %+v, incomplete = %q; want unknown children and an incomplete walk", children, sc.incomplete)
	}
}
