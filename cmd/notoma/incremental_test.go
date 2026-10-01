package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/natikgadzhi/notion-based/internal/notion"
	"github.com/natikgadzhi/notion-based/internal/sync"
	"github.com/natikgadzhi/notion-based/internal/writer"
)

// fakePage is a page served by fakeNotion: its body text, the child pages
// in its blocks and when it was last edited.
type fakePage struct {
	title    string
	text     string
	children []string
	edited   time.Time
	archived bool
	// databases lists child database IDs in the page's blocks.
	databases []string
	// toggle, if set, adds a toggle block with that ID and children.
	toggle string
}

// fakeNotion serves pages, blocks and one database from memory and records
// each request path.
type fakeNotion struct {
	mu       gosync.Mutex
	pages    map[string]*fakePage
	rows     []string // page IDs of the database's rows
	requests []string
	// searchFails makes search return a 500.
	searchFails bool
	// failBlocks makes fetching these blocks' children return a 500.
	failBlocks map[string]bool
	// dbGone makes the database return a 404, dbFails a 500.
	dbGone, dbFails bool
	// failPages makes fetching these pages return a 500.
	failPages map[string]bool
}

func newFakeNotion() *fakeNotion {
	return &fakeNotion{pages: map[string]*fakePage{}, failBlocks: map[string]bool{}, failPages: map[string]bool{}}
}

func (f *fakeNotion) add(id, title, text string, edited time.Time, children ...string) {
	f.pages[id] = &fakePage{title: title, text: text, children: children, edited: edited}
}

func (f *fakeNotion) requestsFor(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == path {
			n++
		}
	}
	return n
}

func (f *fakeNotion) resetRequests() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = nil
}

func (f *fakeNotion) totalRequests() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func richText(s string) []map[string]any {
	return []map[string]any{{"type": "text", "text": map[string]any{"content": s}, "plain_text": s}}
}

func (f *fakeNotion) pageJSON(id string) map[string]any {
	p := f.pages[id]
	return map[string]any{
		"object":           "page",
		"id":               id,
		"last_edited_time": p.edited.Format(time.RFC3339),
		"archived":         p.archived,
		"properties": map[string]any{
			"Name": map[string]any{"id": "title", "type": "title", "title": richText(p.title)},
		},
	}
}

func (f *fakeNotion) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	var id, kind string
	if _, err := fmt.Sscanf(r.URL.Path, "/v1/blocks/%36s", &id); err == nil {
		kind = "blocks"
	} else if _, err := fmt.Sscanf(r.URL.Path, "/v1/pages/%36s", &id); err == nil {
		kind = "page"
	} else if _, err := fmt.Sscanf(r.URL.Path, "/v1/databases/%36s", &id); err == nil {
		kind = "database"
		if r.Method == http.MethodPost {
			kind = "query"
		}
	}

	if r.URL.Path == "/v1/search" {
		kind = "search"
	}

	var body any
	switch kind {
	case "search":
		if f.searchFails {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"object":"error","status":500,"code":"internal_server_error","message":"boom"}`))
			return
		}
		body = map[string]any{"object": "list", "results": []any{}, "has_more": false}
	case "blocks":
		if f.failBlocks[id] {
			serverError(w)
			return
		}
		p, ok := f.pages[id]
		if !ok {
			notFound(w)
			return
		}
		blocks := []map[string]any{{
			"object": "block", "id": id + "-text", "type": "paragraph",
			"paragraph": map[string]any{"rich_text": richText(p.text)},
		}}
		for _, child := range p.children {
			blocks = append(blocks, map[string]any{
				"object": "block", "id": child, "type": "child_page",
				"child_page": map[string]any{"title": f.pages[child].title},
			})
		}
		for _, db := range p.databases {
			blocks = append(blocks, map[string]any{
				"object": "block", "id": db, "type": "child_database",
				"child_database": map[string]any{"title": "Tasks"},
			})
		}
		if p.toggle != "" {
			blocks = append(blocks, map[string]any{
				"object": "block", "id": p.toggle, "type": "toggle", "has_children": true,
				"toggle": map[string]any{"rich_text": richText("more")},
			})
		}
		body = map[string]any{"object": "list", "results": blocks, "has_more": false}
	case "page":
		if f.failPages[id] {
			serverError(w)
			return
		}
		if f.pages[id] == nil {
			notFound(w)
			return
		}
		body = f.pageJSON(id)
	case "database", "query":
		if f.dbGone {
			notFound(w)
			return
		}
		if f.dbFails {
			serverError(w)
			return
		}
		if kind == "query" {
			var rows []map[string]any
			for _, row := range f.rows {
				rows = append(rows, f.pageJSON(row))
			}
			body = map[string]any{"object": "list", "results": rows, "has_more": false}
			break
		}
		body = map[string]any{
			"object": "database", "id": id, "title": richText("Tasks"),
			"last_edited_time": "2025-01-01T00:00:00Z",
			"properties": map[string]any{
				"Name": map[string]any{"id": "title", "name": "Name", "type": "title", "title": map[string]any{}},
			},
		}
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func serverError(w http.ResponseWriter) {
	w.WriteHeader(http.StatusInternalServerError)
	_, _ = w.Write([]byte(`{"object":"error","status":500,"code":"internal_server_error","message":"boom"}`))
}

func notFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"object":"error","status":404,"code":"object_not_found","message":"gone"}`))
}

// lastEdited returns the search sweep's view of the fake workspace, which
// like Notion's leaves out trashed pages.
func (f *fakeNotion) lastEdited() map[string]time.Time {
	times := map[string]time.Time{}
	for id, p := range f.pages {
		if !p.archived {
			times[id] = p.edited
		}
	}
	return times
}

type redirectTransport struct{ target *url.URL }

func (rt redirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = rt.target.Scheme
	req.URL.Host = rt.target.Host
	return http.DefaultTransport.RoundTrip(req)
}

// fakeSyncContext sets up a run against the fake the way runSync does.
func fakeSyncContext(t *testing.T, fake *fakeNotion, state *sync.SyncState, vault, root string) *syncContext {
	t.Helper()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	target, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := notion.NewClientWithHTTPClient("test-token", logger, &http.Client{Transport: redirectTransport{target: target}})
	return &syncContext{
		ctx:              context.Background(),
		client:           client,
		workerPool:       notion.NewWorkerPool(client, 2),
		writer:           writer.New(vault, "_attachments", false, logger),
		logger:           logger,
		state:            state,
		timestampUpdater: sync.NewTimestampUpdater(nil, logger, false, false),
		visited:          map[string]bool{},
		configuredRoots:  map[string]bool{root: true},
		paths:            sync.NewPathAllocator(state),
		lastEdited:       fake.lastEdited(),
		reached:          map[string]bool{},
		reachedEntries:   map[string]map[string]bool{},
		tracked:          state.ResourceCount() + state.EntryCount(),
		rootIDs:          map[string]bool{root: true},
	}
}

// runFakeSync syncs page root from the fake into vault.
func runFakeSync(t *testing.T, fake *fakeNotion, state *sync.SyncState, vault, root string) {
	t.Helper()
	syncFakeRoot(t, fakeSyncContext(t, fake, state, vault, root), fake, root)
}

func syncFakeRoot(t *testing.T, sc *syncContext, fake *fakeNotion, root string) {
	t.Helper()
	p := fake.pages[root]
	resource := &notion.Resource{ID: root, Type: notion.ResourceTypePage, Title: p.title, LastEditedTime: p.edited}
	if err := syncPage(sc, resource, ""); err != nil {
		t.Fatalf("syncPage() error = %v", err)
	}
}

const (
	rootID       = "00000000-0000-0000-0000-0000000000a1"
	childID      = "00000000-0000-0000-0000-0000000000b2"
	grandchildID = "00000000-0000-0000-0000-0000000000c3"
)

var t0 = time.Date(2025, 1, 10, 12, 0, 0, 0, time.UTC)

func blocksPath(id string) string { return "GET /v1/blocks/" + id + "/children" }

// newTree returns root → child → grandchild, synced once into a vault.
func newTree(t *testing.T) (*fakeNotion, *sync.SyncState, string) {
	t.Helper()
	fake := newFakeNotion()
	fake.add(rootID, "Root", "root v1", t0, childID)
	fake.add(childID, "Child", "child v1", t0, grandchildID)
	fake.add(grandchildID, "Grandchild", "grandchild v1", t0)
	state := sync.NewSyncState()
	vault := t.TempDir()
	runFakeSync(t, fake, state, vault, rootID)
	fake.resetRequests()
	return fake, state, vault
}

// editText changes every page's text without bumping its timestamp, so a
// file that still has the old text was not rewritten.
func editText(fake *fakeNotion) {
	for _, p := range fake.pages {
		p.text += " (edited)"
	}
}

func rewritten(t *testing.T, vault, file string) bool {
	t.Helper()
	return strings.Contains(readVaultFile(t, vault, file), "(edited)")
}

func TestIncremental_UnchangedTreeFetchesNoBlocks(t *testing.T) {
	fake, state, vault := newTree(t)
	editText(fake)

	runFakeSync(t, fake, state, vault, rootID)

	if n := fake.totalRequests(); n != 0 {
		t.Errorf("unchanged tree made %d requests %v, want 0", n, fake.requests)
	}
	for _, f := range []string{"Root.md", "Child.md", "Grandchild.md"} {
		if rewritten(t, vault, f) {
			t.Errorf("%s was rewritten", f)
		}
	}
}

func TestIncremental_ChangedGrandchildUnderUnchangedParents(t *testing.T) {
	fake, state, vault := newTree(t)
	editText(fake)
	fake.pages[grandchildID].edited = t0.Add(time.Hour)

	runFakeSync(t, fake, state, vault, rootID)

	if n := fake.totalRequests(); n != 1 || fake.requestsFor(blocksPath(grandchildID)) != 1 {
		t.Errorf("requests = %v, want only the grandchild's blocks", fake.requests)
	}
	if rewritten(t, vault, "Root.md") || rewritten(t, vault, "Child.md") {
		t.Error("unchanged ancestors were rewritten")
	}
	if !rewritten(t, vault, "Grandchild.md") {
		t.Error("changed grandchild was not rewritten")
	}
	if got := state.GetResource(grandchildID).LastModified; !got.Equal(t0.Add(time.Hour)) {
		t.Errorf("grandchild last_modified = %v", got)
	}
}

func TestIncremental_FallsBackToGetPageWhenSearchMissesPage(t *testing.T) {
	fake, state, vault := newTree(t)
	fake.pages[grandchildID].edited = t0.Add(time.Hour)

	sc := fakeSyncContext(t, fake, state, vault, rootID)
	delete(sc.lastEdited, grandchildID)
	syncFakeRoot(t, sc, fake, rootID)

	if fake.requestsFor("GET /v1/pages/"+grandchildID) != 1 || fake.requestsFor(blocksPath(grandchildID)) != 1 {
		t.Errorf("requests = %v, want the grandchild's page then its blocks", fake.requests)
	}
	if got := sc.client.RequestCount(); got != int64(fake.totalRequests()) {
		t.Errorf("RequestCount() = %d, want %d", got, fake.totalRequests())
	}
}

func TestIncremental_OldStateFetchesBlocksOnce(t *testing.T) {
	fake, state, vault := newTree(t)
	// Drop the recorded children, as in a state written before they were.
	for id, res := range state.Resources {
		res.Children = nil
		state.Resources[id] = res
	}

	runFakeSync(t, fake, state, vault, rootID)
	for _, id := range []string{rootID, childID, grandchildID} {
		if fake.requestsFor(blocksPath(id)) != 1 {
			t.Errorf("blocks of %s fetched %d times, want 1", id, fake.requestsFor(blocksPath(id)))
		}
		if state.GetResource(id).Children == nil {
			t.Errorf("children of %s still unknown", id)
		}
	}

	fake.resetRequests()
	runFakeSync(t, fake, state, vault, rootID)
	if n := fake.totalRequests(); n != 0 {
		t.Errorf("second run made %d requests %v, want 0", n, fake.requests)
	}
}

func TestIncremental_RemovedChildDropsFromCache(t *testing.T) {
	fake, state, vault := newTree(t)
	fake.pages[childID].children = nil
	fake.pages[childID].edited = t0.Add(time.Hour)

	runFakeSync(t, fake, state, vault, rootID)

	if got := state.GetResource(childID).Children; got == nil || len(got.Pages) != 0 {
		t.Errorf("child's cached children = %+v, want none", got)
	}
	if fake.requestsFor(blocksPath(grandchildID)) != 0 {
		t.Error("removed grandchild was still fetched")
	}
}

func TestIncremental_UnchangedPageKeepsPathFromNewSameTitledPage(t *testing.T) {
	const newID = "00000000-0000-0000-0000-0000000000d4"
	fake, state, vault := newTree(t)
	editText(fake)
	// A new page titled like the unchanged child appears before it.
	fake.add(newID, "Child", "new page", t0)
	fake.pages[rootID].children = []string{newID, childID}
	fake.pages[rootID].edited = t0.Add(time.Hour)

	runFakeSync(t, fake, state, vault, rootID)

	if p := state.GetResource(childID).LocalPath; p != "Child.md" {
		t.Errorf("unchanged child moved to %q", p)
	}
	if rewritten(t, vault, "Child.md") {
		t.Error("unchanged child's file was rewritten")
	}
	if p := state.GetResource(newID).LocalPath; p != "Child (000000d4).md" {
		t.Errorf("new page path = %q, want the suffixed name", p)
	}
	if got := readVaultFile(t, vault, "Root.md"); !strings.Contains(got, "[[Child (000000d4)|Child]]") || !strings.Contains(got, "[[Child]]") {
		t.Errorf("root links = %q", got)
	}
}

func TestIncremental_UnchangedDatabaseRowWalksCachedChildren(t *testing.T) {
	const (
		dbID  = "00000000-0000-0000-0000-0000000000e5"
		rowID = "00000000-0000-0000-0000-0000000000f6"
	)
	fake := newFakeNotion()
	fake.add(rowID, "Row", "row v1", t0, childID)
	fake.add(childID, "Nested", "nested v1", t0)
	fake.rows = []string{rowID}
	state := sync.NewSyncState()
	vault := t.TempDir()
	syncDB := func() {
		sc := fakeSyncContext(t, fake, state, vault, dbID)
		if err := syncDatabase(sc, &notion.Resource{ID: dbID, Type: notion.ResourceTypeDatabase, Title: "Tasks"}, ""); err != nil {
			t.Fatalf("syncDatabase() error = %v", err)
		}
	}
	syncDB()
	if state.GetEntry(dbID, rowID).Children == nil {
		t.Fatal("row's children were not recorded")
	}

	fake.resetRequests()
	editText(fake)
	fake.pages[childID].edited = t0.Add(time.Hour)
	syncDB()

	if fake.requestsFor(blocksPath(rowID)) != 0 {
		t.Error("unchanged row's blocks were fetched")
	}
	if fake.requestsFor(blocksPath(childID)) != 1 {
		t.Errorf("requests = %v, want the nested page's blocks", fake.requests)
	}
	if rewritten(t, vault, "Tasks/Row.md") || !rewritten(t, vault, "Tasks/Nested.md") {
		t.Error("want only the nested page rewritten")
	}
}

func TestSearchPages_FailureFallsBackWithoutDiscovery(t *testing.T) {
	fake := newFakeNotion()
	fake.add(rootID, "Root", "root v1", t0, childID)
	fake.add(childID, "Child", "child v1", t0)
	fake.searchFails = true
	state := sync.NewSyncState()
	sc := fakeSyncContext(t, fake, state, t.TempDir(), rootID)

	if _, err := searchPages(sc.ctx, sc.client, true, sc.logger); err == nil {
		t.Error("with discovery on, a failed search should be fatal")
	}
	pages, err := searchPages(sc.ctx, sc.client, false, sc.logger)
	if err != nil {
		t.Fatalf("with discovery off, searchPages() error = %v", err)
	}

	sc.lastEdited = notion.LastEditedTimes(pages)
	syncFakeRoot(t, sc, fake, rootID)
	if state.GetResource(rootID) == nil || state.GetResource(childID) == nil {
		t.Error("configured root did not sync after the failed search")
	}
	if fake.requestsFor("GET /v1/pages/"+childID) != 1 {
		t.Errorf("requests = %v, want a GetPage fallback for the child", fake.requests)
	}
}
