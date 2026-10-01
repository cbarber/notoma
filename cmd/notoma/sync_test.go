package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/jomei/notionapi"
	"github.com/natikgadzhi/notion-based/internal/config"
	"github.com/natikgadzhi/notion-based/internal/notion"
	"github.com/natikgadzhi/notion-based/internal/transform"
)

// countingFetcher serves canned block children and counts fetches per block.
type countingFetcher struct {
	children map[string][]notionapi.Block
	errs     map[string]error
	calls    map[string]int
}

func newCountingFetcher(children map[string][]notionapi.Block) *countingFetcher {
	return &countingFetcher{children: children, errs: map[string]error{}, calls: map[string]int{}}
}

func (f *countingFetcher) GetBlockChildren(_ context.Context, blockID string) ([]notionapi.Block, error) {
	f.calls[blockID]++
	if err := f.errs[blockID]; err != nil {
		return nil, err
	}
	return f.children[blockID], nil
}

func basic(id string, typ notionapi.BlockType, hasChildren bool) notionapi.BasicBlock {
	return notionapi.BasicBlock{Object: "block", ID: notionapi.BlockID(id), Type: typ, HasChildren: hasChildren}
}

func childPage(id, title string) *notionapi.ChildPageBlock {
	b := &notionapi.ChildPageBlock{BasicBlock: basic(id, notionapi.BlockTypeChildPage, false)}
	b.ChildPage.Title = title
	return b
}

func childDatabase(id, title string) *notionapi.ChildDatabaseBlock {
	b := &notionapi.ChildDatabaseBlock{BasicBlock: basic(id, notionapi.BlockTypeChildDatabase, false)}
	b.ChildDatabase.Title = title
	return b
}

func paragraph(id string) *notionapi.ParagraphBlock {
	return &notionapi.ParagraphBlock{BasicBlock: basic(id, notionapi.BlockTypeParagraph, false)}
}

func TestCollectChildren(t *testing.T) {
	tests := []struct {
		name      string
		blocks    []notionapi.Block
		children  map[string][]notionapi.Block
		pages     []childPageInfo
		databases []childPageInfo
	}{
		{
			name:   "empty blocks",
			blocks: []notionapi.Block{},
		},
		{
			name:   "no child pages",
			blocks: []notionapi.Block{paragraph("para-1"), &notionapi.Heading1Block{BasicBlock: basic("h1-1", notionapi.BlockTypeHeading1, false)}},
		},
		{
			name:      "top-level child pages and databases",
			blocks:    []notionapi.Block{childPage("page-1", "First"), paragraph("para-1"), childDatabase("db-1", "Inline DB"), childPage("page-2", "Second")},
			pages:     []childPageInfo{{"page-1", "First"}, {"page-2", "Second"}},
			databases: []childPageInfo{{"db-1", "Inline DB"}},
		},
		{
			name:   "column list and column",
			blocks: []notionapi.Block{&notionapi.ColumnListBlock{BasicBlock: basic("cols", notionapi.BlockTypeColumnList, true)}},
			children: map[string][]notionapi.Block{
				"cols":  {&notionapi.ColumnBlock{BasicBlock: basic("col-1", notionapi.BlockTypeColumn, true)}, &notionapi.ColumnBlock{BasicBlock: basic("col-2", notionapi.BlockTypeColumn, true)}},
				"col-1": {childPage("handbook", "Handbook")},
				"col-2": {childDatabase("digest", "Digest")},
			},
			pages:     []childPageInfo{{"handbook", "Handbook"}},
			databases: []childPageInfo{{"digest", "Digest"}},
		},
		{
			name:     "toggle",
			blocks:   []notionapi.Block{&notionapi.ToggleBlock{BasicBlock: basic("toggle", notionapi.BlockTypeToggle, true)}},
			children: map[string][]notionapi.Block{"toggle": {childPage("p", "In Toggle")}},
			pages:    []childPageInfo{{"p", "In Toggle"}},
		},
		{
			name:     "toggleable heading",
			blocks:   []notionapi.Block{&notionapi.Heading2Block{BasicBlock: basic("h2", notionapi.BlockTypeHeading2, true)}},
			children: map[string][]notionapi.Block{"h2": {childPage("p", "In Heading")}},
			pages:    []childPageInfo{{"p", "In Heading"}},
		},
		{
			name:      "callout",
			blocks:    []notionapi.Block{&notionapi.CalloutBlock{BasicBlock: basic("callout", notionapi.BlockTypeCallout, true)}},
			children:  map[string][]notionapi.Block{"callout": {childDatabase("db", "In Callout")}},
			databases: []childPageInfo{{"db", "In Callout"}},
		},
		{
			name:     "bulleted list item",
			blocks:   []notionapi.Block{&notionapi.BulletedListItemBlock{BasicBlock: basic("li", notionapi.BlockTypeBulletedListItem, true)}},
			children: map[string][]notionapi.Block{"li": {childPage("p", "In List")}},
			pages:    []childPageInfo{{"p", "In List"}},
		},
		{
			name:     "original synced block",
			blocks:   []notionapi.Block{&notionapi.SyncedBlock{BasicBlock: basic("synced", notionapi.BlockTypeSyncedBlock, true)}},
			children: map[string][]notionapi.Block{"synced": {childPage("p", "In Synced")}},
			pages:    []childPageInfo{{"p", "In Synced"}},
		},
		{
			name: "synced block reference reads the original",
			blocks: []notionapi.Block{func() notionapi.Block {
				b := &notionapi.SyncedBlock{BasicBlock: basic("ref", notionapi.BlockTypeSyncedBlock, true)}
				b.SyncedBlock.SyncedFrom = &notionapi.SyncedFrom{BlockID: "original"}
				return b
			}()},
			children: map[string][]notionapi.Block{"original": {childPage("p", "From Original")}, "ref": {childPage("wrong", "Wrong")}},
			pages:    []childPageInfo{{"p", "From Original"}},
		},
		{
			name: "nested three levels deep, mixed with top level, in document order",
			blocks: []notionapi.Block{
				childPage("top-1", "Top 1"),
				&notionapi.ColumnListBlock{BasicBlock: basic("cols", notionapi.BlockTypeColumnList, true)},
				childPage("top-2", "Top 2"),
			},
			children: map[string][]notionapi.Block{
				"cols":   {&notionapi.ColumnBlock{BasicBlock: basic("col", notionapi.BlockTypeColumn, true)}},
				"col":    {&notionapi.ToggleBlock{BasicBlock: basic("toggle", notionapi.BlockTypeToggle, true)}, childPage("level-2", "Level 2")},
				"toggle": {childPage("level-3", "Level 3"), childDatabase("db-3", "DB 3")},
			},
			pages:     []childPageInfo{{"top-1", "Top 1"}, {"level-3", "Level 3"}, {"level-2", "Level 2"}, {"top-2", "Top 2"}},
			databases: []childPageInfo{{"db-3", "DB 3"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fetcher := newCountingFetcher(tt.children)
			pages, databases, err := collectChildren(context.Background(), fetcher, tt.blocks)
			if err != nil {
				t.Fatalf("collectChildren() error = %v", err)
			}
			assertChildren(t, "pages", pages, tt.pages)
			assertChildren(t, "databases", databases, tt.databases)
		})
	}
}

func assertChildren(t *testing.T, kind string, got, want []childPageInfo) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d %s %v, want %d %v", len(got), kind, got, len(want), want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %v, want %v", kind, i, got[i], want[i])
		}
	}
}

func TestCollectChildren_DoesNotDescendIntoChildPagesOrDatabases(t *testing.T) {
	page := childPage("page", "Page")
	page.HasChildren = true
	db := childDatabase("db", "DB")
	db.HasChildren = true
	fetcher := newCountingFetcher(nil)

	if _, _, err := collectChildren(context.Background(), fetcher, []notionapi.Block{page, db}); err != nil {
		t.Fatalf("collectChildren() error = %v", err)
	}
	if len(fetcher.calls) != 0 {
		t.Errorf("fetched children of %v, want no fetches", fetcher.calls)
	}
}

func TestCollectChildren_ContinuesPastFetchErrors(t *testing.T) {
	errToggle := errors.New("toggle boom")
	errCallout := errors.New("callout boom")
	fetcher := newCountingFetcher(map[string][]notionapi.Block{
		"col": {&notionapi.CalloutBlock{BasicBlock: basic("callout", notionapi.BlockTypeCallout, true)}, childPage("in-col", "In Column")},
	})
	fetcher.errs["toggle"] = errToggle
	fetcher.errs["callout"] = errCallout
	blocks := []notionapi.Block{
		childPage("before", "Before"),
		&notionapi.ToggleBlock{BasicBlock: basic("toggle", notionapi.BlockTypeToggle, true)},
		&notionapi.ColumnBlock{BasicBlock: basic("col", notionapi.BlockTypeColumn, true)},
		childPage("after", "After"),
	}

	pages, _, err := collectChildren(context.Background(), fetcher, blocks)
	if !errors.Is(err, errToggle) || !errors.Is(err, errCallout) {
		t.Fatalf("collectChildren() error = %v, want both fetch errors", err)
	}
	assertChildren(t, "pages", pages, []childPageInfo{{"before", "Before"}, {"in-col", "In Column"}, {"after", "After"}})
}

func TestBlockCache_ReusesTransformerFetches(t *testing.T) {
	blocks := []notionapi.Block{&notionapi.ColumnListBlock{BasicBlock: basic("cols", notionapi.BlockTypeColumnList, true)}}
	fetcher := newCountingFetcher(map[string][]notionapi.Block{
		"cols": {&notionapi.ColumnBlock{BasicBlock: basic("col", notionapi.BlockTypeColumn, true)}},
		"col":  {childPage("handbook", "Handbook")},
	})
	cache := newBlockCache(fetcher)

	transformer := transform.NewTransformer(context.Background(), cache)
	if _, err := transformer.BlocksToMarkdown(blocks); err != nil {
		t.Fatalf("BlocksToMarkdown() error = %v", err)
	}
	pages, _, err := collectChildren(context.Background(), cache, blocks)
	if err != nil {
		t.Fatalf("collectChildren() error = %v", err)
	}

	assertChildren(t, "pages", pages, []childPageInfo{{"handbook", "Handbook"}})
	for id, n := range fetcher.calls {
		if n != 1 {
			t.Errorf("children of %q fetched %d times, want 1", id, n)
		}
	}
}

func TestSyncChildDatabases(t *testing.T) {
	var logs bytes.Buffer
	sc := &syncContext{
		ctx:             context.Background(),
		logger:          slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
		visited:         map[string]bool{"already": true},
		configuredRoots: map[string]bool{"reading-list": true},
	}
	parent := &notion.Resource{ID: "parent", Title: "Engineering"}
	var synced []string
	syncDB := func(sc *syncContext, resource *notion.Resource, folderName string) error {
		if resource.Type != notion.ResourceTypeDatabase {
			t.Errorf("resource type = %q, want database", resource.Type)
		}
		synced = append(synced, resource.ID+":"+resource.Title)
		switch resource.ID {
		case "linked":
			return fmt.Errorf("fetching database: %w", &notionapi.Error{Status: 404, Code: "object_not_found"})
		case "broken":
			return errors.New("boom")
		}
		return nil
	}

	syncChildDatabases(sc, parent, []childPageInfo{
		{"digest", "Engineering Digest"},
		{"already", "Root DB"},
		{"reading-list", "Reading List"},
		{"linked", "Linked View"},
		{"broken", "Broken"},
	}, syncDB)

	want := []string{"digest:Engineering Digest", "linked:Linked View", "broken:Broken"}
	if strings.Join(synced, ",") != strings.Join(want, ",") {
		t.Errorf("synced %v, want %v", synced, want)
	}

	// Only the non-404 failure is reported as an error.
	errorLines := strings.Count(logs.String(), "level=ERROR")
	if errorLines != 1 || !strings.Contains(logs.String(), "database=Broken") {
		t.Errorf("error logs = %q, want exactly one, for Broken", logs.String())
	}
}

func TestConfiguredRootIDs(t *testing.T) {
	ids := configuredRootIDs([]config.Root{
		{URL: "https://www.notion.so/myworkspace/Reading-List-def456789012345678901234567890ab?v=1"},
		{URL: "not a notion url"},
	})
	if len(ids) != 1 || !ids["def45678-9012-3456-7890-1234567890ab"] {
		t.Errorf("configuredRootIDs() = %v, want only the parsed UUID", ids)
	}
}

func TestIsInaccessibleError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"not found", fmt.Errorf("wrapped: %w", &notionapi.Error{Status: 404}), true},
		{"forbidden", &notionapi.Error{Status: 403}, true},
		{"server error", &notionapi.Error{Status: 500}, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInaccessibleError(tt.err); got != tt.want {
				t.Errorf("isInaccessibleError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple name",
			input:    "My Page",
			expected: "My Page",
		},
		{
			name:     "name with slashes",
			input:    "Parent/Child",
			expected: "Parent-Child",
		},
		{
			name:     "name with special chars",
			input:    "File: Test?",
			expected: "File- Test",
		},
		{
			name:     "name with newlines",
			input:    "Line1\nLine2",
			expected: "Line1 Line2",
		},
		{
			name:     "name with leading/trailing spaces",
			input:    "  Spaced  ",
			expected: "Spaced",
		},
		{
			name:     "very long name",
			input:    string(make([]byte, 250)),
			expected: string(make([]byte, 200)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := transform.SanitizeFilename(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeFilename(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestIsUUIDPrefix(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "valid UUID prefix",
			input:    "1e567c00...",
			expected: true,
		},
		{
			name:     "valid hex only",
			input:    "abcdef12",
			expected: true,
		},
		{
			name:     "too short",
			input:    "abc",
			expected: false,
		},
		{
			name:     "not hex",
			input:    "MyPageNa",
			expected: false,
		},
		{
			name:     "mixed case hex fails (uppercase)",
			input:    "ABCDEF12",
			expected: false,
		},
		{
			name:     "real page title",
			input:    "My Notes Page",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isUUIDPrefix(tt.input)
			if result != tt.expected {
				t.Errorf("isUUIDPrefix(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}
