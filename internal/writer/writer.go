// Package writer handles writing synced content to the Obsidian vault.
package writer

import (
	"bufio"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Writer handles writing files to the Obsidian vault.
type Writer struct {
	vaultPath        string
	attachmentFolder string
	dryRun           bool
	logger           *slog.Logger
}

// New creates a new Writer instance.
func New(vaultPath, attachmentFolder string, dryRun bool, logger *slog.Logger) *Writer {
	return &Writer{
		vaultPath:        vaultPath,
		attachmentFolder: attachmentFolder,
		dryRun:           dryRun,
		logger:           logger,
	}
}

// WriteMarkdown writes a markdown file to the vault.
// folderPath is relative to the vault root (can be empty for root).
// filename should include .md extension.
// content is the full file content (frontmatter + body).
func (w *Writer) WriteMarkdown(folderPath, filename, content string) error {
	fullPath := filepath.Join(w.vaultPath, folderPath, filename)

	if w.dryRun {
		w.logger.Info("would write", "path", fullPath, "size", len(content))
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	// Write file
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("writing file %s: %w", fullPath, err)
	}

	w.logger.Debug("wrote file", "path", fullPath, "size", len(content))
	return nil
}

// WriteBase writes a .base file for a database.
// folderPath is relative to the vault root.
// name is the database name (without extension).
// content is the YAML content for the .base file.
func (w *Writer) WriteBase(folderPath, name string, content []byte) error {
	filename := name + ".base"
	fullPath := filepath.Join(w.vaultPath, folderPath, filename)

	if w.dryRun {
		w.logger.Info("would write base", "path", fullPath, "size", len(content))
		return nil
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	// Write file
	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		return fmt.Errorf("writing file %s: %w", fullPath, err)
	}

	w.logger.Debug("wrote base file", "path", fullPath, "size", len(content))
	return nil
}

// EnsureFolder creates a folder in the vault if it doesn't exist.
func (w *Writer) EnsureFolder(folderPath string) error {
	fullPath := filepath.Join(w.vaultPath, folderPath)

	if w.dryRun {
		w.logger.Debug("would ensure folder", "path", fullPath)
		return nil
	}

	if err := os.MkdirAll(fullPath, 0o755); err != nil {
		return fmt.Errorf("creating folder %s: %w", fullPath, err)
	}

	return nil
}

// WriteAttachment writes an attachment file to the attachment folder.
// localPath is relative to the vault root (includes attachment folder).
// Returns the full path to the written file.
func (w *Writer) WriteAttachment(localPath string, data []byte) (string, error) {
	fullPath := filepath.Join(w.vaultPath, localPath)

	if w.dryRun {
		w.logger.Info("would write attachment", "path", fullPath, "size", len(data))
		return fullPath, nil
	}

	// Ensure directory exists
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("creating attachment directory %s: %w", dir, err)
	}

	// Write file
	if err := os.WriteFile(fullPath, data, 0o644); err != nil {
		return "", fmt.Errorf("writing attachment %s: %w", fullPath, err)
	}

	w.logger.Debug("wrote attachment", "path", fullPath, "size", len(data))
	return fullPath, nil
}

// AttachmentExists checks if an attachment file already exists.
func (w *Writer) AttachmentExists(localPath string) bool {
	fullPath := filepath.Join(w.vaultPath, localPath)
	_, err := os.Stat(fullPath)
	return err == nil
}

// DeleteAttachment removes an attachment file.
func (w *Writer) DeleteAttachment(localPath string) error {
	fullPath := filepath.Join(w.vaultPath, localPath)

	if w.dryRun {
		w.logger.Info("would delete attachment", "path", fullPath)
		return nil
	}

	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("deleting attachment %s: %w", fullPath, err)
	}

	w.logger.Debug("deleted attachment", "path", fullPath)
	return nil
}

// GetVaultPath returns the vault path.
func (w *Writer) GetVaultPath() string {
	return w.vaultPath
}

// GetAttachmentFolder returns the attachment folder name.
func (w *Writer) GetAttachmentFolder() string {
	return w.attachmentFolder
}

// NotionID returns the notion-id recorded in the frontmatter of the file at
// relPath, or "" if the file doesn't exist or records none. The legacy
// notion_id key written by older versions is also accepted.
func (w *Writer) NotionID(relPath string) (string, error) {
	if !inVault(relPath) {
		return "", fmt.Errorf("reading %q: path is outside the vault", relPath)
	}
	f, err := os.Open(filepath.Join(w.vaultPath, relPath))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() || scanner.Text() != "---" {
		return "", scanner.Err()
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "---" {
			break
		}
		key, value, ok := strings.Cut(line, ":")
		if ok && (key == "notion-id" || key == "notion_id") {
			return strings.Trim(strings.TrimSpace(value), `"'`), nil
		}
	}
	return "", scanner.Err()
}

// inVault reports whether relPath names something inside the vault, not
// the vault itself or anything outside it.
func inVault(relPath string) bool {
	return filepath.IsLocal(relPath) && filepath.Clean(relPath) != "."
}

// Remove deletes the file at relPath; a missing file is not an error.
func (w *Writer) Remove(relPath string) error {
	if !inVault(relPath) {
		return fmt.Errorf("removing %q: path is outside the vault", relPath)
	}
	fullPath := filepath.Join(w.vaultPath, relPath)
	if w.dryRun {
		w.logger.Info("would remove", "path", fullPath)
		return nil
	}
	if err := os.Remove(fullPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing %s: %w", fullPath, err)
	}
	w.logger.Debug("removed file", "path", fullPath)
	return nil
}

// Move renames the file or folder at from to to, both relative to the
// vault. A missing source is not an error; an existing target is.
func (w *Writer) Move(from, to string) error {
	// Paths come from Notion titles; never act on the vault itself or
	// anything outside it.
	if !inVault(from) || !inVault(to) {
		return fmt.Errorf("moving %q to %q: path is outside the vault", from, to)
	}
	src := filepath.Join(w.vaultPath, from)
	dst := filepath.Join(w.vaultPath, to)
	if w.dryRun {
		w.logger.Info("would move", "from", src, "to", dst)
		return nil
	}
	if _, err := os.Stat(src); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// os.Rename silently replaces files, so refuse an existing target
	// unless it is the source itself (a case-only rename on APFS).
	if dstInfo, err := os.Lstat(dst); err == nil {
		srcInfo, srcErr := os.Lstat(src)
		if srcErr != nil || !os.SameFile(srcInfo, dstInfo) {
			return fmt.Errorf("moving %s: %s already exists", src, dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("creating directory for %s: %w", dst, err)
	}
	if err := os.Rename(src, dst); err != nil {
		return fmt.Errorf("moving %s to %s: %w", src, dst, err)
	}
	w.logger.Debug("moved", "from", src, "to", dst)
	return nil
}
