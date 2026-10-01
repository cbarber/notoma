package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jomei/notionapi"
	"github.com/natikgadzhi/notion-based/internal/config"
	"github.com/natikgadzhi/notion-based/internal/notion"
	"github.com/natikgadzhi/notion-based/internal/sync"
	"github.com/natikgadzhi/notion-based/internal/transform"
	"github.com/natikgadzhi/notion-based/internal/tui"
	"github.com/natikgadzhi/notion-based/internal/writer"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	dryRun bool
	force  bool
	quiet  bool // quiet disables TUI and shows plain log output
)

// syncContext holds dependencies for sync operations, reducing parameter count.
type syncContext struct {
	ctx              context.Context
	client           *notion.Client
	workerPool       *notion.WorkerPool
	writer           *writer.Writer
	logger           *slog.Logger
	state            *sync.SyncState
	tuiRunner        *tui.Runner
	attDownloader    *transform.AttachmentDownloader
	dateFormatter    *transform.DateFormatter
	timestampUpdater *sync.TimestampUpdater
	dryRun           bool
	// visited spans the whole run so a page or database reachable from
	// several roots, or both as a root and as an inline child, syncs once.
	visited map[string]bool
	// configuredRoots holds the IDs of roots listed in the config.
	configuredRoots map[string]bool
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync Notion content to Obsidian vault",
	Long: `Sync fetches pages and databases from Notion and converts them
to Obsidian-flavored markdown files in your vault.

By default, it performs incremental sync - only fetching pages
modified since the last sync. Use --force to perform a full resync.

When running in a terminal, a TUI progress display is shown by default.
Use --quiet to disable the TUI and show plain log output instead.
Use --verbose to enable debug logging (shown alongside TUI or in quiet mode).`,
	RunE: runSync,
}

func init() {
	syncCmd.Flags().StringVarP(&configPath, "config", "c", "config.yaml", "path to config file")
	syncCmd.Flags().BoolVarP(&dryRun, "dry-run", "n", false, "preview changes without writing files")
	syncCmd.Flags().BoolVarP(&force, "force", "f", false, "ignore state and perform full resync")
	syncCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "enable verbose logging")
	syncCmd.Flags().BoolVarP(&quiet, "quiet", "q", false, "disable TUI, use plain log output")
}

func runSync(cmd *cobra.Command, args []string) error {
	// Determine if we should use TUI mode
	// Use TUI by default if stdout is a TTY and quiet mode is not enabled
	useTUI := !quiet && term.IsTerminal(int(os.Stdout.Fd()))

	// Set up logging - suppress in TUI mode unless verbose
	var logOutput io.Writer = os.Stderr
	if useTUI && !verbose {
		logOutput = io.Discard
	}
	logger := setupLogger(logOutput, verbose)

	// Set up context with signal handling
	ctx, cancel := setupSignalHandler(logger)
	defer cancel()

	// Load configuration
	logger.Info("loading configuration", "path", configPath)
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	if dryRun {
		logger.Info("dry-run mode enabled, no files will be written")
	}

	// Compute config hash for change detection
	configHash := sync.ComputeConfigHash(sync.ConfigSettings{
		DownloadAttachments: cfg.Options.ShouldDownloadAttachments(),
		AttachmentFolder:    cfg.Output.AttachmentFolder,
	})

	// Load sync state (or create new if --force or doesn't exist)
	var state *sync.SyncState
	if force {
		logger.Info("force mode enabled, ignoring state and performing full resync")
		state = sync.NewSyncState()
		state.UpdateConfigHash(configHash)
	} else {
		state, err = sync.LoadState(cfg.State.File)
		if err != nil {
			return fmt.Errorf("loading state: %w", err)
		}

		// Check if config changed since last sync
		if state.CheckConfigChanged(configHash) {
			logger.Info("config changed since last sync, invalidating state for full resync")
			state.InvalidateForConfigChange(configHash)
		} else {
			// Update hash for new state files or unchanged config
			state.UpdateConfigHash(configHash)
		}

		if state.ResourceCount() > 0 {
			logger.Info("loaded sync state",
				"resources", state.ResourceCount(),
				"entries", state.EntryCount(),
				"last_sync", state.LastSyncTime.Format(time.RFC3339),
			)
		} else {
			logger.Info("no previous sync state found, performing full sync")
		}
	}

	// Create Notion client
	client := notion.NewClient(cfg.NotionToken, logger)

	// Create worker pool for parallel fetching (5 concurrent workers)
	workerPool := notion.DefaultWorkerPool(client)

	// Note: OnStart callback will be set after TUI runner is created

	// Validate connection by fetching current user
	user, err := client.GetCurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("validating Notion token: %w", err)
	}
	logger.Info("connected to Notion", "bot", user.Name)

	// Create writer
	w := writer.New(cfg.Output.VaultPath, cfg.Output.AttachmentFolder, dryRun, logger)

	// Create attachment downloader if enabled (defaults to true)
	var attDownloader *transform.AttachmentDownloader
	if cfg.Options.ShouldDownloadAttachments() {
		attDownloader = transform.NewAttachmentDownloader(cfg.Output.AttachmentFolder, dryRun, logger)
		logger.Info("attachment downloading enabled", "folder", cfg.Output.AttachmentFolder)
	}

	// Create TUI runner if in TUI mode
	var tuiRunner *tui.Runner
	if useTUI {
		tuiRunner = tui.NewRunner()
		if err := tuiRunner.Start(); err != nil {
			return fmt.Errorf("starting TUI: %w", err)
		}

		// Set up worker pool callback to mark items as syncing when work actually starts
		workerPool.SetOnStart(func(pageID string) {
			tuiRunner.SetSyncing(pageID)
		})
	}

	// Build list of roots to process
	roots := cfg.Sync.Roots

	// Discover workspace roots if enabled
	if cfg.Sync.DiscoverWorkspaceRoots {
		logger.Info("discovering workspace roots")
		discovered, err := client.DiscoverWorkspaceRoots(ctx)
		if err != nil {
			return fmt.Errorf("discovering workspace roots: %w", err)
		}
		logger.Info("discovered workspace roots", "count", len(discovered))

		// Convert discovered resources to config.Root format
		for _, res := range discovered {
			// Use Notion URL format for discovered roots
			url := fmt.Sprintf("https://notion.so/%s", strings.ReplaceAll(res.ID, "-", ""))
			roots = append(roots, config.Root{
				URL:  url,
				Name: res.Title,
			})
		}
	}

	// Create date formatter from config
	dateFormatter := transform.NewDateFormatter(cfg.Options.GetDatesConfig())

	// Create timestamp updater if enabled
	timestampUpdater := sync.NewTimestampUpdater(client, logger, cfg.Options.ShouldUpdateNotionTimestamp(), dryRun)
	if timestampUpdater.IsEnabled() {
		logger.Info("Notion timestamp updates enabled")
	}

	// Create sync context to pass to sync functions
	sc := &syncContext{
		ctx:              ctx,
		client:           client,
		workerPool:       workerPool,
		writer:           w,
		logger:           logger,
		state:            state,
		tuiRunner:        tuiRunner,
		attDownloader:    attDownloader,
		dateFormatter:    dateFormatter,
		timestampUpdater: timestampUpdater,
		dryRun:           dryRun,
		visited:          make(map[string]bool),
		configuredRoots:  configuredRootIDs(cfg.Sync.Roots),
	}

	// Attachments are written before each checkpoint so the state never
	// records a page as synced while its attachments exist only in memory.
	written := make(map[string]bool)
	var flushAttachments func()
	if attDownloader != nil {
		flushAttachments = func() {
			writeAttachments(ctx, attDownloader, w, state, written, logger)
		}
	}
	checkpointer := sync.NewCheckpointer(ctx, cfg.State.File, state, sync.CheckpointInterval, dryRun, flushAttachments, logger)
	state.OnChange(checkpointer.Record)

	// Process each root
	var syncErr error
	for _, root := range roots {
		if ctx.Err() != nil {
			break
		}
		if err := processRoot(sc, root); err != nil {
			logger.Error("failed to process root", "url", root.URL, "error", err)
			syncErr = err
			// Continue with other roots
		}
	}

	if err := ctx.Err(); err != nil {
		logger.Info("sync interrupted, saving progress")
		syncErr = err
	}

	if err := checkpointer.Save(); err != nil {
		logger.Error("failed to save state", "error", err)
		if syncErr == nil {
			syncErr = err
		}
	} else if !dryRun {
		logger.Info("saved sync state", "path", cfg.State.File)
	}

	// Signal completion to TUI
	if tuiRunner != nil {
		tuiRunner.Done(syncErr)
		tuiRunner.Wait()
	} else {
		logger.Info("sync complete")
	}

	return syncErr
}

// configuredRootIDs returns the IDs of the roots listed in the config.
// Unparseable URLs are left for processRoot to report.
func configuredRootIDs(roots []config.Root) map[string]bool {
	ids := make(map[string]bool, len(roots))
	for _, root := range roots {
		parsed, err := notion.ParseURL(root.URL)
		if err != nil {
			continue
		}
		ids[parsed.ID] = true
	}
	return ids
}

// writeAttachments writes attachments downloaded since the last call and
// records them in state. written tracks URLs already handled this run.
func writeAttachments(ctx context.Context, d *transform.AttachmentDownloader, w *writer.Writer, state *sync.SyncState, written map[string]bool, logger *slog.Logger) {
	// Ignore cancellation: after Ctrl-C the pages referencing these
	// attachments are saved as synced, so a skipped fetch is never retried.
	// The HTTP client's timeout still bounds each download.
	ctx = context.WithoutCancel(ctx)
	count := 0
	for url, att := range d.GetDownloaded() {
		if written[url] {
			continue
		}
		data, err := d.GetData(ctx, url)
		if err != nil {
			logger.Error("failed to download attachment data", "url", url, "error", err)
			continue
		}
		if _, err := w.WriteAttachment(att.LocalPath, data); err != nil {
			logger.Error("failed to write attachment", "path", att.LocalPath, "error", err)
			continue
		}
		written[url] = true
		state.UpdateAttachmentState(url, att.ContentHash, att.LocalPath, att.Size, "")
		count++
	}
	if count > 0 {
		logger.Info("downloaded attachments", "count", count)
	}
}

func processRoot(sc *syncContext, root config.Root) error {
	// Parse URL to get ID
	parsed, err := notion.ParseURL(root.URL)
	if err != nil {
		return fmt.Errorf("parsing URL: %w", err)
	}

	name := root.Name
	if name == "" {
		name = parsed.ID[:8] + "..."
	}
	sc.logger.Info("processing root", "name", name, "id", parsed.ID)

	// Detect resource type
	resource, err := sc.client.DetectResourceType(sc.ctx, parsed.ID)
	if err != nil {
		return fmt.Errorf("detecting resource type: %w", err)
	}

	sc.logger.Info("detected resource",
		"type", resource.Type,
		"title", resource.Title,
		"id", resource.ID,
	)

	// Add to TUI if available (starts as pending, worker pool OnStart marks as syncing)
	if sc.tuiRunner != nil {
		itemType := tui.TypePage
		if resource.Type == notion.ResourceTypeDatabase {
			itemType = tui.TypeDatabase
		}
		sc.tuiRunner.AddRoot(resource.ID, resource.Title, resource.Icon, itemType)
	}

	var syncErr error
	switch resource.Type {
	case notion.ResourceTypePage:
		// Pages are written directly to the vault root as flat md files
		syncErr = syncPage(sc, resource, "")

	case notion.ResourceTypeDatabase:
		syncErr = syncDatabase(sc, resource, name)
	}

	// Update TUI status
	if sc.tuiRunner != nil {
		if syncErr != nil {
			sc.tuiRunner.SetError(resource.ID, syncErr.Error())
		} else {
			sc.tuiRunner.SetDone(resource.ID)
		}
	}

	return syncErr
}

// syncPage syncs a standalone page to the vault.
// folderPath specifies where to write the page (empty for vault root).
func syncPage(sc *syncContext, resource *notion.Resource, folderPath string) error {
	return syncPageRecursive(sc, resource, folderPath, sc.visited)
}

// syncPageRecursive syncs a page and all its child pages recursively.
// visited tracks already-synced page IDs to prevent infinite loops.
func syncPageRecursive(sc *syncContext, resource *notion.Resource, folderPath string, visited map[string]bool) error {
	// Check for cycles
	if visited[resource.ID] {
		sc.logger.Debug("skipping already visited page", "id", resource.ID, "title", resource.Title)
		return nil
	}
	visited[resource.ID] = true

	// Mark as syncing for root-level pages (not going through worker pool)
	if sc.tuiRunner != nil {
		sc.tuiRunner.SetSyncing(resource.ID)
	}

	// Fetch page metadata to get LastEditedTime
	page, err := sc.client.GetPage(sc.ctx, resource.ID)
	if err != nil {
		return fmt.Errorf("fetching page: %w", err)
	}

	lastModified := page.LastEditedTime

	// Check if sync is needed
	if !sc.state.NeedsSync(resource.ID, lastModified) {
		sc.logger.Info("page unchanged, skipping", "title", resource.Title)
		if sc.tuiRunner != nil {
			sc.tuiRunner.SetDone(resource.ID)
		}
		return nil
	}

	// Fetch blocks
	blocks, err := sc.client.GetBlockChildren(sc.ctx, resource.ID)
	if err != nil {
		return fmt.Errorf("fetching page blocks: %w", err)
	}

	filename := transform.SanitizeFilename(resource.Title) + ".md"
	localPath := filename
	if folderPath != "" {
		localPath = folderPath + "/" + filename
	}

	fetcher := newBlockCache(sc.client)
	if sc.dryRun {
		sc.logger.Info("would sync page", "title", resource.Title, "blocks", len(blocks), "path", localPath)
	} else {
		// Transform blocks to markdown (with attachment downloading and date formatting)
		transformer := transform.NewTransformerWithOptions(sc.ctx, fetcher, sc.attDownloader, sc.dateFormatter)

		markdown, err := transformer.BlocksToMarkdown(blocks)
		if err != nil {
			return fmt.Errorf("transforming blocks: %w", err)
		}

		// Write markdown file
		if err := sc.writer.WriteMarkdown(folderPath, filename, markdown); err != nil {
			return fmt.Errorf("writing markdown: %w", err)
		}

		// Update state
		sc.state.SetResource(sync.ResourceState{
			ID:           resource.ID,
			Type:         sync.ResourceTypePage,
			Title:        resource.Title,
			LastModified: lastModified,
			LocalPath:    localPath,
		})

		// Update timestamp in Notion
		_ = sc.timestampUpdater.UpdateAfterSync(sc.ctx, resource.ID)

		sc.logger.Info("synced page", "title", resource.Title, "file", localPath)
	}

	syncNestedChildren(sc, resource, blocks, fetcher, folderPath, visited)
	return nil
}

// processChildPageWithBlocks processes a child page with pre-fetched blocks.
// It writes the markdown file and recursively processes grandchildren.
func processChildPageWithBlocks(sc *syncContext, resource *notion.Resource, blocks []notionapi.Block, lastModified time.Time, folderPath string, visited map[string]bool) error {
	// Check if sync is needed
	if !sc.state.NeedsSync(resource.ID, lastModified) {
		sc.logger.Info("child page unchanged, skipping", "title", resource.Title)
		return nil
	}

	filename := transform.SanitizeFilename(resource.Title) + ".md"
	localPath := filename
	if folderPath != "" {
		localPath = folderPath + "/" + filename
	}

	fetcher := newBlockCache(sc.client)
	if sc.dryRun {
		sc.logger.Info("would sync child page", "title", resource.Title, "blocks", len(blocks), "path", localPath)
	} else {
		// Transform blocks to markdown (with attachment downloading and date formatting)
		transformer := transform.NewTransformerWithOptions(sc.ctx, fetcher, sc.attDownloader, sc.dateFormatter)

		markdown, err := transformer.BlocksToMarkdown(blocks)
		if err != nil {
			return fmt.Errorf("transforming blocks: %w", err)
		}

		// Write markdown file
		if err := sc.writer.WriteMarkdown(folderPath, filename, markdown); err != nil {
			return fmt.Errorf("writing markdown: %w", err)
		}

		// Update state
		sc.state.SetResource(sync.ResourceState{
			ID:           resource.ID,
			Type:         sync.ResourceTypePage,
			Title:        resource.Title,
			LastModified: lastModified,
			LocalPath:    localPath,
		})

		// Update timestamp in Notion
		_ = sc.timestampUpdater.UpdateAfterSync(sc.ctx, resource.ID)

		sc.logger.Info("synced child page", "title", resource.Title, "file", localPath)
	}

	syncNestedChildren(sc, resource, blocks, fetcher, folderPath, visited)
	return nil
}

// syncChildPages fetches the given child pages in parallel and syncs each
// one, recursing into their own children.
func syncChildPages(sc *syncContext, parent *notion.Resource, childPages []childPageInfo, folderPath string, visited map[string]bool) {
	// Children go in the same folder as their parent (flat structure for Obsidian)
	childFolder := folderPath

	// Build map of child info and list of IDs to fetch
	childMap := make(map[string]childPageInfo)
	var childIDs []string
	for _, child := range childPages {
		// Skip already visited pages
		if visited[child.id] {
			sc.logger.Debug("skipping already visited child", "id", child.id)
			continue
		}
		childMap[child.id] = child
		childIDs = append(childIDs, child.id)

		// Add to TUI (starts as pending, worker pool OnStart marks as syncing)
		// Note: child pages from blocks don't have icon data, pass empty for default
		if sc.tuiRunner != nil {
			sc.tuiRunner.AddChild(parent.ID, child.id, child.title, "", tui.TypePage)
		}
	}
	if len(childIDs) == 0 {
		return
	}

	// Fetch child page data (metadata + blocks) in parallel
	results := sc.workerPool.FetchPagesWithBlocksParallel(sc.ctx, childIDs)
	for result := range results {
		child := childMap[result.PageID]

		if result.Err != nil {
			sc.logger.Error("failed to fetch child page", "id", result.PageID, "error", result.Err)
			if sc.tuiRunner != nil {
				sc.tuiRunner.SetError(result.PageID, result.Err.Error())
			}
			continue
		}

		// Mark as visited
		visited[result.PageID] = true

		childResource := &notion.Resource{
			ID:    result.PageID,
			Type:  notion.ResourceTypePage,
			Title: child.title,
		}

		// Recursively process the child
		if err := processChildPageWithBlocks(sc, childResource, result.Blocks, result.Page.LastEditedTime, childFolder, visited); err != nil {
			sc.logger.Error("failed to sync child page", "parent", parent.Title, "child", child.title, "error", err)
			if sc.tuiRunner != nil {
				sc.tuiRunner.SetError(result.PageID, err.Error())
			}
		} else if sc.tuiRunner != nil {
			sc.tuiRunner.SetDone(result.PageID)
		}
	}
}

// childPageInfo holds basic info about a child page found in blocks.
type childPageInfo struct {
	id    string
	title string
}

// blockCache memoizes block children per page so that discovering nested
// children reuses the fetches the transformer already made for rendering.
type blockCache struct {
	fetcher  transform.BlockFetcher
	children map[string][]notionapi.Block
}

func newBlockCache(fetcher transform.BlockFetcher) *blockCache {
	return &blockCache{fetcher: fetcher, children: make(map[string][]notionapi.Block)}
}

// GetBlockChildren implements transform.BlockFetcher.
func (c *blockCache) GetBlockChildren(ctx context.Context, blockID string) ([]notionapi.Block, error) {
	if blocks, ok := c.children[blockID]; ok {
		return blocks, nil
	}
	blocks, err := c.fetcher.GetBlockChildren(ctx, blockID)
	if err != nil {
		return nil, err
	}
	c.children[blockID] = blocks
	return blocks, nil
}

// collectChildren walks the whole block tree depth-first and returns the
// child pages and child databases in document order. Notion nests them
// inside columns, toggles, callouts, list items and synced blocks, not
// only at the top level. Fetch errors are joined and returned alongside
// everything that could still be found.
func collectChildren(ctx context.Context, fetcher transform.BlockFetcher, blocks []notionapi.Block) (pages, databases []childPageInfo, err error) {
	var errs []error
	for _, block := range blocks {
		var nestedID string
		switch b := block.(type) {
		case *notionapi.ChildPageBlock:
			pages = append(pages, childPageInfo{id: string(b.ID), title: b.ChildPage.Title})
			continue
		case *notionapi.ChildDatabaseBlock:
			databases = append(databases, childPageInfo{id: string(b.ID), title: b.ChildDatabase.Title})
			continue
		case *notionapi.SyncedBlock:
			// A synced block reference renders the original's children,
			// matching the transformer.
			nestedID = string(b.ID)
			if b.SyncedBlock.SyncedFrom != nil && b.SyncedBlock.SyncedFrom.BlockID != "" {
				nestedID = string(b.SyncedBlock.SyncedFrom.BlockID)
			} else if !b.HasChildren {
				continue
			}
		default:
			if !block.GetHasChildren() {
				continue
			}
			nestedID = string(block.GetID())
		}

		nested, fetchErr := fetcher.GetBlockChildren(ctx, nestedID)
		if fetchErr != nil {
			// Keep scanning siblings: one unreadable block (e.g. a synced
			// block whose original isn't shared) shouldn't hide the rest.
			errs = append(errs, fmt.Errorf("fetching children of block %s: %w", nestedID, fetchErr))
			continue
		}
		nestedPages, nestedDatabases, nestedErr := collectChildren(ctx, fetcher, nested)
		pages = append(pages, nestedPages...)
		databases = append(databases, nestedDatabases...)
		if nestedErr != nil {
			errs = append(errs, nestedErr)
		}
	}
	return pages, databases, errors.Join(errs...)
}

// syncNestedChildren syncs every child page and child database found in
// a page's block tree.
func syncNestedChildren(sc *syncContext, parent *notion.Resource, blocks []notionapi.Block, fetcher transform.BlockFetcher, folderPath string, visited map[string]bool) {
	childPages, childDatabases, err := collectChildren(sc.ctx, fetcher, blocks)
	if err != nil {
		// Still sync the children that could be found.
		sc.logger.Error("failed to scan nested blocks for children", "parent", parent.Title, "error", err)
	}
	if len(childPages) > 0 {
		sc.logger.Debug("found child pages", "parent", parent.Title, "count", len(childPages), "folder", folderPath)
		syncChildPages(sc, parent, childPages, folderPath, visited)
	}
	syncChildDatabases(sc, parent, childDatabases, syncDatabase)
}

// databaseSyncFunc syncs a database; injected so tests can observe calls.
type databaseSyncFunc func(sc *syncContext, resource *notion.Resource, folderName string) error

// syncChildDatabases syncs inline databases found in a page's blocks.
func syncChildDatabases(sc *syncContext, parent *notion.Resource, childDatabases []childPageInfo, syncDB databaseSyncFunc) {
	for _, child := range childDatabases {
		if sc.visited[child.id] {
			sc.logger.Debug("skipping already visited database", "id", child.id)
			continue
		}
		// A configured root syncs under its configured name when its own
		// root is processed, whatever the config order.
		if sc.configuredRoots[child.id] {
			sc.logger.Debug("leaving configured root database to its root", "id", child.id)
			continue
		}
		if sc.tuiRunner != nil {
			sc.tuiRunner.AddChild(parent.ID, child.id, child.title, "", tui.TypeDatabase)
		}

		resource := &notion.Resource{ID: child.id, Type: notion.ResourceTypeDatabase, Title: child.title}
		err := syncDB(sc, resource, "")
		if err == nil {
			if sc.tuiRunner != nil {
				sc.tuiRunner.SetDone(child.id)
			}
			continue
		}

		// Linked database views of databases the integration can't access
		// come back as 404/403; they aren't syncable, so they aren't errors.
		if isInaccessibleError(err) {
			sc.logger.Debug("skipping inaccessible child database", "parent", parent.Title, "database", child.title, "error", err)
			if sc.tuiRunner != nil {
				sc.tuiRunner.SetDone(child.id)
			}
			continue
		}
		sc.logger.Error("failed to sync child database", "parent", parent.Title, "database", child.title, "error", err)
		if sc.tuiRunner != nil {
			sc.tuiRunner.SetError(child.id, err.Error())
		}
	}
}

// isInaccessibleError reports whether err is a Notion 404 or 403 response.
func isInaccessibleError(err error) bool {
	var apiErr *notionapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusNotFound || apiErr.Status == http.StatusForbidden
}

// syncDatabase syncs a database and all its entries to the vault.
func syncDatabase(sc *syncContext, resource *notion.Resource, folderName string) error {
	if sc.visited[resource.ID] {
		sc.logger.Debug("skipping already visited database", "id", resource.ID, "title", resource.Title)
		return nil
	}
	sc.visited[resource.ID] = true

	// Mark root database as syncing
	if sc.tuiRunner != nil {
		sc.tuiRunner.SetSyncing(resource.ID)
	}

	// Fetch database schema
	db, err := sc.client.GetDatabase(sc.ctx, resource.ID)
	if err != nil {
		return fmt.Errorf("fetching database: %w", err)
	}

	schema, err := transform.ParseDatabaseSchema(db)
	if err != nil {
		return fmt.Errorf("parsing database schema: %w", err)
	}

	// Determine folder path for entries
	folder := transform.SanitizeFilename(resource.Title)
	if folderName != "" && !isUUIDPrefix(folderName) {
		folder = transform.SanitizeFilename(folderName)
	}

	// Query database entries
	pages, err := sc.client.QueryDatabase(sc.ctx, resource.ID)
	if err != nil {
		return fmt.Errorf("querying database: %w", err)
	}

	// Initialize or get database state
	dbState := sc.state.GetResource(resource.ID)
	if dbState == nil {
		sc.state.SetResource(sync.ResourceState{
			ID:           resource.ID,
			Type:         sync.ResourceTypeDatabase,
			Title:        resource.Title,
			LastModified: db.LastEditedTime,
			LocalPath:    folder,
			Entries:      make(map[string]sync.EntryState),
		})
		dbState = sc.state.GetResource(resource.ID)
	}

	// Add child entries to TUI
	if sc.tuiRunner != nil {
		for _, page := range pages {
			title := notion.ExtractPageTitle(&page)
			if title == "" {
				title = string(page.ID)[:8] + "..."
			}
			icon := notion.ExtractPageIcon(&page)
			sc.tuiRunner.AddChild(resource.ID, string(page.ID), title, icon, tui.TypePage)
		}
	}

	if sc.dryRun {
		sc.logger.Info("would sync database", "title", resource.Title, "folder", folder, "entries", len(pages))
		for i, page := range pages {
			if i >= 10 {
				sc.logger.Info("... and more", "remaining", len(pages)-10)
				break
			}
			sc.logger.Info("  entry", "title", notion.ExtractPageTitle(&page))
		}
		return nil
	}

	// Generate and write .base file
	baseFile, err := transform.GenerateBaseFile(schema, folder)
	if err != nil {
		return fmt.Errorf("generating base file: %w", err)
	}
	baseContent, err := transform.MarshalBaseFile(baseFile)
	if err != nil {
		return fmt.Errorf("marshaling base file: %w", err)
	}
	if err := sc.writer.WriteBase("", resource.Title, baseContent); err != nil {
		return fmt.Errorf("writing base file: %w", err)
	}

	// Ensure folder exists for entries
	if err := sc.writer.EnsureFolder(folder); err != nil {
		return fmt.Errorf("creating folder: %w", err)
	}

	// Filter pages that need syncing and build lookup map
	var pagesToSync []string
	pageMap := make(map[string]*notionapi.Page)
	syncedCount := 0
	skippedCount := 0

	for i := range pages {
		page := &pages[i]
		pageID := string(page.ID)
		pageMap[pageID] = page

		// Check if entry needs sync
		if !sc.state.NeedsEntrySync(resource.ID, pageID, page.LastEditedTime) {
			sc.logger.Debug("entry unchanged, skipping", "id", pageID)
			skippedCount++
			if sc.tuiRunner != nil {
				sc.tuiRunner.SetDone(pageID)
			}
			continue
		}

		pagesToSync = append(pagesToSync, pageID)
		// Note: Item stays as pending until worker pool OnStart callback marks it as syncing
	}

	sc.logger.Info("fetching blocks in parallel",
		"to_sync", len(pagesToSync),
		"skipped", skippedCount,
	)

	// Fetch blocks in parallel using worker pool
	if len(pagesToSync) > 0 {
		results := sc.workerPool.FetchBlocksParallel(sc.ctx, pagesToSync)

		// Process results as they arrive
		for result := range results {
			page := pageMap[result.PageID]
			pageID := result.PageID

			if result.Err != nil {
				sc.logger.Error("failed to fetch blocks", "id", pageID, "error", result.Err)
				if sc.tuiRunner != nil {
					sc.tuiRunner.SetError(pageID, result.Err.Error())
				}
				continue
			}

			// Process the entry with pre-fetched blocks; the cache is per
			// entry so the nested-children scan below reuses its fetches.
			fetcher := newBlockCache(sc.client)
			transformer := transform.NewTransformerWithOptions(sc.ctx, fetcher, sc.attDownloader, sc.dateFormatter)
			filename, err := syncDatabaseEntryWithBlocks(sc, transformer, page, result.Blocks, schema, folder)
			if err != nil {
				sc.logger.Error("failed to sync entry", "id", pageID, "error", err)
				if sc.tuiRunner != nil {
					sc.tuiRunner.SetError(pageID, err.Error())
				}
				continue
			}

			syncedCount++
			// Update entry state
			_ = sc.state.SetEntry(resource.ID, sync.EntryState{
				PageID:       pageID,
				Title:        notion.ExtractPageTitle(page),
				LastModified: page.LastEditedTime,
				LocalFile:    filename,
			})

			// Update timestamp in Notion
			_ = sc.timestampUpdater.UpdateAfterSync(sc.ctx, pageID)

			if sc.tuiRunner != nil {
				sc.tuiRunner.SetDone(pageID)
			}

			entry := &notion.Resource{ID: pageID, Type: notion.ResourceTypePage, Title: notion.ExtractPageTitle(page)}
			syncNestedChildren(sc, entry, result.Blocks, fetcher, folder, sc.visited)
		}
	}

	// Update database state with latest timestamp
	sc.state.SetResource(sync.ResourceState{
		ID:           resource.ID,
		Type:         sync.ResourceTypeDatabase,
		Title:        resource.Title,
		LastModified: db.LastEditedTime,
		LocalPath:    folder,
		Entries:      dbState.Entries,
	})

	sc.logger.Info("synced database",
		"title", resource.Title,
		"folder", folder,
		"synced", syncedCount,
		"skipped", skippedCount,
		"total", len(pages),
	)
	return nil
}

// syncDatabaseEntryWithBlocks syncs a single database entry using pre-fetched blocks.
// Returns the filename written and any error.
func syncDatabaseEntryWithBlocks(sc *syncContext, transformer *transform.Transformer, page *notionapi.Page, blocks []notionapi.Block, schema *transform.DatabaseSchema, folder string) (string, error) {
	// Extract entry data for frontmatter using the transformer's date formatter
	entry, err := transform.ExtractEntryData(page, schema, transformer.GetDateFormatter())
	if err != nil {
		return "", fmt.Errorf("extracting entry data: %w", err)
	}

	// Transform blocks to markdown
	markdown, err := transformer.BlocksToMarkdown(blocks)
	if err != nil {
		return "", fmt.Errorf("transforming blocks: %w", err)
	}

	// Build complete entry with frontmatter
	dbEntry, err := transform.BuildDatabaseEntry(entry, markdown)
	if err != nil {
		return "", fmt.Errorf("building entry: %w", err)
	}

	// Write the file
	content := dbEntry.Frontmatter + "\n" + dbEntry.Content
	if err := sc.writer.WriteMarkdown(folder, dbEntry.Filename, content); err != nil {
		return "", fmt.Errorf("writing entry: %w", err)
	}

	sc.logger.Debug("synced entry", "title", entry.Title, "file", dbEntry.Filename)
	return dbEntry.Filename, nil
}

// isUUIDPrefix checks if the name looks like a truncated UUID (e.g., "1e567c00...").
func isUUIDPrefix(name string) bool {
	if len(name) < 8 {
		return false
	}
	// Check if first 8 chars are hex
	for _, c := range name[:8] {
		isDigit := c >= '0' && c <= '9'
		isHexLower := c >= 'a' && c <= 'f'
		if !isDigit && !isHexLower {
			return false
		}
	}
	return true
}
