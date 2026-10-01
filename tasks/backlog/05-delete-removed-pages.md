# Remove local files for pages deleted or unshared in Notion

## Problem

When a page or database row is deleted (or no longer accessible) in
Notion, its local file stays forever. `DetectDeletedResources` and
`DetectDeletedEntries` exist in `internal/sync/state.go` but are never
called.

## Scope

- After a full, successful run, any resource or entry in state that was
  not reached by the walk is removed: delete its file (or empty database
  folder plus `.base`), drop it from state, log it.
- Safety rails:
  - Skip deletion entirely if any root failed, the run was cancelled, or
    `--dry-run` (dry-run logs what would be deleted).
  - Only delete a file whose frontmatter `notion-id` matches the state
    entry (task 03); otherwise log and leave it.
  - If more than 20% of tracked resources would be deleted in one run,
    abort deletion and log a warning (guards against token/permission
    mistakes). A config option or flag to override is not required.
- Attachments referenced only by deleted pages are out of scope.

## Acceptance criteria

- Unit tests: deleted page removed; deleted database row removed;
  failed root → nothing deleted; dry-run → nothing deleted; >20%
  threshold → nothing deleted; mismatched `notion-id` file kept.
- E2E (procedure in task 02): deleting real Notion content is not
  allowed — simulate by adding a fake resource to the scratch state with
  a matching file and confirm it is removed.
- README documents deletion behaviour and the safety threshold.

## Dependencies

Blocked by 04 (needs the complete set of reached IDs even when
unchanged pages skip block fetches).
