package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jomei/notionapi"
	"github.com/natikgadzhi/notion-based/internal/notion"
	"github.com/natikgadzhi/notion-based/internal/sync"
	"github.com/natikgadzhi/notion-based/internal/transform"
	"github.com/natikgadzhi/notion-based/internal/writer"
)

func newPathsTestContext(t *testing.T, state *sync.SyncState) (*syncContext, string) {
	t.Helper()
	vault := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &syncContext{
		ctx:              context.Background(),
		writer:           writer.New(vault, "_attachments", false, logger),
		logger:           logger,
		state:            state,
		timestampUpdater: sync.NewTimestampUpdater(nil, logger, false, false),
		visited:          map[string]bool{},
		configuredRoots:  map[string]bool{},
		paths:            sync.NewPathAllocator(state),
	}, vault
}

func writeVaultFile(t *testing.T, vault, rel, content string) {
	t.Helper()
	full := filepath.Join(vault, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readVaultFile(t *testing.T, vault, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(vault, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func exists(vault, rel string) bool {
	_, err := os.Stat(filepath.Join(vault, rel))
	return err == nil
}

func TestWritePage_RenameRemovesOldFile(t *testing.T) {
	const id = "page-1"
	state := sync.NewSyncState()
	state.SetResource(sync.ResourceState{ID: id, Type: sync.ResourceTypePage, LocalPath: "Old Title.md"})
	sc, vault := newPathsTestContext(t, state)
	writeVaultFile(t, vault, "Old Title.md", transform.PageFrontmatter(id)+"\nold\n")

	resource := &notion.Resource{ID: id, Type: notion.ResourceTypePage, Title: "New Title"}
	got, err := writePage(sc, resource, "", []notionapi.Block{paragraph("p")}, nil, time.Now(), nil)
	if err != nil {
		t.Fatalf("writePage() error = %v", err)
	}

	if got != "New Title.md" {
		t.Errorf("writePage() path = %q, want New Title.md", got)
	}
	if exists(vault, "Old Title.md") {
		t.Error("old file still exists after rename")
	}
	if content := readVaultFile(t, vault, "New Title.md"); !strings.HasPrefix(content, "---\nnotion-id: page-1\n---\n") {
		t.Errorf("new file frontmatter = %q", content)
	}
	if p := state.GetResource(id).LocalPath; p != "New Title.md" {
		t.Errorf("state path = %q, want New Title.md", p)
	}
}

func TestWritePage_KeepsOldPathOwnedByAnotherPage(t *testing.T) {
	const id = "page-1"
	state := sync.NewSyncState()
	state.SetResource(sync.ResourceState{ID: id, Type: sync.ResourceTypePage, LocalPath: "Old Title.md"})
	sc, vault := newPathsTestContext(t, state)
	foreign := transform.PageFrontmatter("page-2") + "\nsomeone else\n"
	writeVaultFile(t, vault, "Old Title.md", foreign)
	writeVaultFile(t, vault, "Untracked.md", "no frontmatter\n")

	resource := &notion.Resource{ID: id, Type: notion.ResourceTypePage, Title: "New Title"}
	if _, err := writePage(sc, resource, "", nil, nil, time.Now(), nil); err != nil {
		t.Fatalf("writePage() error = %v", err)
	}
	if got := readVaultFile(t, vault, "Old Title.md"); got != foreign {
		t.Errorf("file owned by another page was changed: %q", got)
	}

	sc.removeStale(id, "Untracked.md", "New Title.md")
	if !exists(vault, "Untracked.md") {
		t.Error("file without notion-id was deleted")
	}
}

func TestWritePage_CaseOnlyRenameChangesName(t *testing.T) {
	const id = "page-1"
	state := sync.NewSyncState()
	state.SetResource(sync.ResourceState{ID: id, Type: sync.ResourceTypePage, LocalPath: "notes.md"})
	sc, vault := newPathsTestContext(t, state)
	writeVaultFile(t, vault, "notes.md", transform.PageFrontmatter(id)+"\nold\n")

	if _, err := writePage(sc, &notion.Resource{ID: id, Title: "Notes"}, "", nil, nil, time.Now(), nil); err != nil {
		t.Fatalf("writePage() error = %v", err)
	}

	entries, err := os.ReadDir(vault)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "Notes.md" {
		t.Errorf("vault files = %v, want only Notes.md", names)
	}
}

func TestAllocate_UnsafeNamesBecomeUntitled(t *testing.T) {
	sc, _ := newPathsTestContext(t, sync.NewSyncState())
	for i, name := range []string{"", ".", ".."} {
		id := fmt.Sprintf("db-%d", i)
		got := sc.allocate(id, "", name, "")
		if !strings.HasPrefix(got, "Untitled") {
			t.Errorf("allocate(%q) = %q, want an Untitled folder", name, got)
		}
	}
}

func TestMoveDatabaseFolder(t *testing.T) {
	state := sync.NewSyncState()
	state.SetResource(sync.ResourceState{ID: "db", Type: sync.ResourceTypeDatabase, LocalPath: "Action Items"})
	sc, vault := newPathsTestContext(t, state)
	base, err := transform.GenerateBaseFile(&transform.DatabaseSchema{}, "Action Items")
	if err != nil {
		t.Fatal(err)
	}
	content, err := transform.MarshalBaseFile(base)
	if err != nil {
		t.Fatal(err)
	}
	writeVaultFile(t, vault, "Action Items.base", string(content))
	writeVaultFile(t, vault, "Action Items/Task.md", "task")

	if err := sc.moveDatabaseFolder("Action Items", "Action Items (abc)"); err != nil {
		t.Fatalf("moveDatabaseFolder() error = %v", err)
	}

	if !exists(vault, "Action Items (abc)/Task.md") || exists(vault, "Action Items") || exists(vault, "Action Items.base") {
		t.Error("folder and base file were not moved")
	}
	if got := readVaultFile(t, vault, "Action Items (abc).base"); !strings.Contains(got, `inFolder("Action Items (abc)")`) {
		t.Errorf("moved base filter = %q", got)
	}
	if p := state.GetResource("db").LocalPath; p != "Action Items (abc)" {
		t.Errorf("state path = %q", p)
	}
}

func TestChildPageLinkTarget_UsesAllocatedName(t *testing.T) {
	state := sync.NewSyncState()
	state.SetResource(sync.ResourceState{ID: "owner", Type: sync.ResourceTypePage, LocalPath: "Engineering.md"})
	sc, _ := newPathsTestContext(t, state)
	sc.configuredRoots["root"] = true

	page := func(id string) notionapi.Block { return childPage(id, "Engineering") }
	md, err := sc.newTransformer(nil, "Wiki").BlocksToMarkdown([]notionapi.Block{page("child"), page("root")})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(md, "[[Wiki/Engineering|Engineering]]") {
		t.Errorf("child in Wiki folder should link by its folder path: %q", md)
	}
	if !strings.Contains(md, "[[Engineering (root)|Engineering]]") {
		t.Errorf("configured root at the vault root should link to its suffixed name: %q", md)
	}
	if p := sc.paths.Allocate("root", "Ignored", "Ignored", ".md"); p != "Engineering (root).md" {
		t.Errorf("configured root allocated at %q, want the vault root", p)
	}
}

func TestSyncChildPages_SkipsConfiguredRoots(t *testing.T) {
	sc, _ := newPathsTestContext(t, sync.NewSyncState())
	sc.configuredRoots["root"] = true

	// No worker pool: reaching the fetch would panic, so this also proves
	// nothing was fetched.
	syncChildPages(sc, &notion.Resource{ID: "entry", Title: "Entry"}, []childPageInfo{{"root", "Root Page"}}, "Database", sc.visited)

	if sc.visited["root"] {
		t.Error("configured root was synced as a child")
	}
}
