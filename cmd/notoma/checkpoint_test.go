package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/natikgadzhi/notion-based/internal/sync"
	"github.com/natikgadzhi/notion-based/internal/transform"
	"github.com/natikgadzhi/notion-based/internal/writer"
)

func TestWriteAttachments_WritesBeforeRecording(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path == "/missing.png" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()

	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := t.TempDir()
	d := transform.NewAttachmentDownloader("_attachments", false, logger)
	w := writer.New(vault, "_attachments", false, logger)
	state := sync.NewSyncState()
	written := make(map[string]bool)

	okURL := srv.URL + "/ok.png"
	att, err := d.Download(ctx, okURL, transform.AttachmentTypeImage)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	writeAttachments(ctx, d, w, state, written, logger)

	if _, err := os.Stat(filepath.Join(vault, att.LocalPath)); err != nil {
		t.Fatalf("attachment should be on disk: %v", err)
	}
	if !state.HasAttachment(okURL) {
		t.Error("written attachment should be recorded in state")
	}

	// An attachment whose data can't be fetched must not be recorded,
	// and attachments already written are not fetched again.
	missingURL := srv.URL + "/missing.png"
	d.GetDownloaded()[missingURL] = &transform.Attachment{OriginalURL: missingURL, LocalPath: "_attachments/missing.png"}
	before := requests
	writeAttachments(ctx, d, w, state, written, logger)

	if state.HasAttachment(missingURL) {
		t.Error("attachment that failed to write must not be recorded in state")
	}
	if got := requests - before; got != 1 {
		t.Errorf("expected only the new attachment to be fetched, got %d requests", got)
	}
}

func TestWriteAttachments_CancelledContextStillWrites(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("image-bytes"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vault := t.TempDir()
	d := transform.NewAttachmentDownloader("_attachments", false, logger)
	w := writer.New(vault, "_attachments", false, logger)
	state := sync.NewSyncState()

	url := srv.URL + "/ok.png"
	att, err := d.Download(ctx, url, transform.AttachmentTypeImage)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}

	cancel()
	writeAttachments(ctx, d, w, state, make(map[string]bool), logger)

	if _, err := os.Stat(filepath.Join(vault, att.LocalPath)); err != nil {
		t.Fatalf("attachment should be on disk after cancellation: %v", err)
	}
	if !state.HasAttachment(url) {
		t.Error("attachment written after cancellation should be recorded in state")
	}
}
