package writer

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func newTestWriter(t *testing.T) (*Writer, string) {
	t.Helper()
	vault := t.TempDir()
	return New(vault, "_attachments", false, slog.New(slog.NewTextHandler(io.Discard, nil))), vault
}

func TestNotionID(t *testing.T) {
	w, vault := newTestWriter(t)
	files := map[string]string{
		"page.md":   "---\nnotion-id: abc-123\n---\n\nbody\n",
		"legacy.md": "---\ntitle: x\nnotion_id: \"def-456\"\n---\n",
		"plain.md":  "no frontmatter\nnotion-id: nope\n",
		"inbody.md": "---\ntitle: x\n---\nnotion-id: nope\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(vault, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	want := map[string]string{"page.md": "abc-123", "legacy.md": "def-456", "plain.md": "", "inbody.md": "", "missing.md": ""}
	for name, w2 := range want {
		got, err := w.NotionID(name)
		if err != nil || got != w2 {
			t.Errorf("NotionID(%s) = %q, %v; want %q", name, got, err, w2)
		}
	}
}

func TestMove(t *testing.T) {
	w, vault := newTestWriter(t)
	if err := os.WriteFile(filepath.Join(vault, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "b.md"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := w.Move("a.md", "b.md"); err == nil {
		t.Error("Move() onto an existing file succeeded, want error")
	}
	if err := w.Move("a.md", "sub/c.md"); err != nil {
		t.Fatalf("Move() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "sub/c.md")); err != nil {
		t.Errorf("moved file missing: %v", err)
	}
	for _, bad := range [][2]string{{"", "e"}, {".", "e"}, {"..", "e"}, {"../x", "e"}, {"a.md", "../e"}, {"/tmp/x", "e"}} {
		if err := w.Move(bad[0], bad[1]); err == nil {
			t.Errorf("Move(%q, %q) succeeded, want error", bad[0], bad[1])
		}
	}
	if err := w.Move("missing.md", "d.md"); err != nil {
		t.Errorf("Move() of missing source error = %v, want nil", err)
	}
}

func TestRemove(t *testing.T) {
	w, vault := newTestWriter(t)
	if err := os.WriteFile(filepath.Join(vault, "a.md"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.Remove("a.md"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(vault, "a.md")); !os.IsNotExist(err) {
		t.Errorf("file still exists after Remove()")
	}
	if err := w.Remove("a.md"); err != nil {
		t.Errorf("Remove() of missing file error = %v, want nil", err)
	}
	for _, bad := range []string{"", ".", "..", "../a.md", "/tmp/a.md"} {
		if err := w.Remove(bad); err == nil {
			t.Errorf("Remove(%q) succeeded, want error", bad)
		}
		if _, err := w.NotionID(bad); err == nil {
			t.Errorf("NotionID(%q) succeeded, want error", bad)
		}
	}
}
