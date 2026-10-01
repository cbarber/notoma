package main

import (
	"errors"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/natikgadzhi/notion-based/internal/sync"
)

// maxRemovedFraction caps how much of the state one run may delete. A
// revoked share or a wrong token makes everything look removed; deleting
// most of a vault for that would be far worse than keeping stale files.
const maxRemovedFraction = 0.2

// removeIfComplete runs removeUnreached unless the run failed or the walk
// may have missed resources that still exist.
func removeIfComplete(sc *syncContext, syncErr error) {
	switch {
	case syncErr != nil:
		sc.logger.Warn("skipping deletion of removed pages: the sync did not finish cleanly", "error", syncErr)
	case sc.incomplete != "":
		sc.logger.Warn("skipping deletion of removed pages: the walk was incomplete", "reason", sc.incomplete)
	default:
		if missing := missingRoots(sc.state.Roots, sc.rootIDs); len(missing) > 0 && !sc.prune {
			sc.logger.Warn("skipping deletion of removed pages: roots synced before are missing from this run, rerun with --prune to remove their pages",
				"missing", missing)
			return
		}
		removeUnreached(sc)
		if !sc.dryRun {
			sc.state.Roots = slices.Sorted(maps.Keys(sc.rootIDs))
		}
	}
}

// missingRoots returns the roots of an earlier run absent from this one.
// Their pages would all look removed, though a dropped config entry or a
// flaky discovery search is the likelier cause.
func missingRoots(previous []string, current map[string]bool) []string {
	var missing []string
	for _, id := range previous {
		if !current[id] {
			missing = append(missing, id)
		}
	}
	return missing
}

// removeUnreached deletes the files of pages, databases and rows the state
// tracks but this run's walk did not reach, and drops them from state.
// Callers must only run it after a complete walk.
func removeUnreached(sc *syncContext) {
	resources := sc.state.DetectDeletedResources(sc.reached)
	var entries []sync.EntryChange
	removing := len(resources)
	for _, res := range resources {
		if res.Type == sync.ResourceTypeDatabase {
			removing += len(sc.state.GetResource(res.ID).Entries)
		}
	}
	for dbID, rows := range sc.reachedEntries {
		entries = append(entries, sc.state.DetectDeletedEntries(dbID, rows)...)
	}
	removing += len(entries)
	if removing == 0 {
		return
	}

	if float64(removing) > maxRemovedFraction*float64(sc.tracked) {
		sc.logger.Warn("not removing pages: too many are missing from Notion, check the token and sharing",
			"removing", removing, "tracked", sc.tracked)
		return
	}

	for _, e := range entries {
		db := sc.state.GetResource(e.DatabaseID)
		file := path.Join(db.LocalPath, db.Entries[e.PageID].LocalFile)
		if sc.dryRun {
			sc.logger.Info("would remove database entry", "title", e.Title, "file", file)
			continue
		}
		sc.removeFile(e.PageID, file)
		sc.state.RemoveEntry(e.DatabaseID, e.PageID)
	}
	for _, r := range resources {
		res := sc.state.GetResource(r.ID)
		if sc.dryRun {
			sc.logger.Info("would remove "+string(r.Type), "title", r.Title, "path", res.LocalPath)
			continue
		}
		if r.Type == sync.ResourceTypeDatabase {
			sc.removeDatabase(res)
		} else {
			sc.removeFile(r.ID, res.LocalPath)
		}
		sc.state.RemoveResource(r.ID)
	}
}

// removeDatabase deletes a database's rows, its .base file and its folder
// once that is empty.
func (sc *syncContext) removeDatabase(res *sync.ResourceState) {
	for id, entry := range res.Entries {
		sc.removeFile(id, path.Join(res.LocalPath, entry.LocalFile))
	}
	if res.LocalPath == "" {
		return
	}
	if err := sc.writer.Remove(res.LocalPath + ".base"); err != nil {
		sc.logger.Error("failed to remove base file", "path", res.LocalPath+".base", "error", err)
	}
	dir := filepath.Join(sc.writer.GetVaultPath(), res.LocalPath)
	if left, err := os.ReadDir(dir); err != nil || len(left) > 0 {
		return
	}
	if err := sc.writer.Remove(res.LocalPath); err != nil {
		sc.logger.Error("failed to remove database folder", "path", res.LocalPath, "error", err)
		return
	}
	sc.logger.Info("removed database", "title", res.Title, "folder", res.LocalPath)
}

// removeFile deletes the file at p only if its notion-id says it is id's,
// so a file someone else now keeps at that path survives.
func (sc *syncContext) removeFile(id, p string) {
	if p == "" {
		return
	}
	owner, err := sc.writer.NotionID(p)
	if err != nil {
		sc.logger.Error("failed to read file before removing it", "path", p, "error", err)
		return
	}
	if owner != id {
		if _, statErr := os.Stat(filepath.Join(sc.writer.GetVaultPath(), p)); !errors.Is(statErr, os.ErrNotExist) {
			sc.logger.Warn("leaving file of removed page, its notion-id does not match", "path", p, "id", id, "notion_id", owner)
		}
		return
	}
	if err := sc.writer.Remove(p); err != nil {
		sc.logger.Error("failed to remove file", "path", p, "error", err)
		return
	}
	sc.logger.Info("removed page deleted from Notion", "path", p)
}
