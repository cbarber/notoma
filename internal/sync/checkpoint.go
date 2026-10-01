package sync

import (
	"context"
	"log/slog"
)

// CheckpointInterval is how many synced resources accumulate between
// checkpoint saves. It bounds the work lost to a crash while keeping
// state writes rare relative to Notion API calls.
const CheckpointInterval = 100

// Checkpointer persists sync state periodically during a run, so an
// interrupted sync keeps the progress made so far.
type Checkpointer struct {
	ctx        context.Context
	path       string
	state      *SyncState
	every      int
	pending    int
	dryRun     bool
	beforeSave func()
	logger     *slog.Logger
}

// NewCheckpointer creates a Checkpointer that saves state to path every
// `every` recorded resources. beforeSave, if non-nil, runs before each save
// so that files the state refers to (attachments) reach disk first.
func NewCheckpointer(ctx context.Context, path string, state *SyncState, every int, dryRun bool, beforeSave func(), logger *slog.Logger) *Checkpointer {
	return &Checkpointer{
		ctx:        ctx,
		path:       path,
		state:      state,
		every:      every,
		dryRun:     dryRun,
		beforeSave: beforeSave,
		logger:     logger,
	}
}

// Record counts one synced resource. It saves once the interval is
// reached, and on every call after ctx is cancelled so that resources
// still finishing during shutdown are not lost.
func (c *Checkpointer) Record() {
	c.pending++
	if c.pending < c.every && c.ctx.Err() == nil {
		return
	}
	// A failed checkpoint must not abort the sync; the final Save reports errors.
	if err := c.Save(); err != nil {
		c.logger.Error("failed to checkpoint state", "error", err)
		return
	}
	c.logger.Debug("checkpointed sync state", "path", c.path, "resources", c.state.ResourceCount())
}

// Save writes the state to disk unless in dry-run mode.
func (c *Checkpointer) Save() error {
	if c.dryRun {
		return nil
	}
	if c.beforeSave != nil {
		c.beforeSave()
	}
	if err := SaveState(c.path, c.state); err != nil {
		return err
	}
	c.pending = 0
	return nil
}
