# Keep walking children of unchanged pages during incremental sync

## Problem

`syncPageRecursive` returns early when a page's `last_edited_time` is
unchanged, before it looks at children. A nested page's edit does not
bump its parent's `last_edited_time`, so edits below an unchanged parent
are never synced without `--force`. It also calls `GetPage` once per
page just to read `last_edited_time`.

## Scope

- Store each page's discovered children (page and database IDs + titles)
  in its `ResourceState`. When a page is unchanged, skip fetching its
  blocks and recurse into the cached children instead.
- Once per run, sweep `POST /v1/search` (filter object=page, sorted by
  `last_edited_time` desc, page_size 100) to build an ID →
  `last_edited_time` map. Use it in place of per-page `GetPage` calls
  for the unchanged check; fall back to `GetPage` for IDs not in the map
  (search index lag or pages not returned by search).
- `SearchAll` already exists in `internal/notion/client.go`; workspace
  root discovery already performs this sweep — reuse its results rather
  than searching twice.
- State format change must be backward compatible: an old state without
  cached children behaves as "needs block fetch" for that page.
- Database row change detection is unchanged (rows are queried each run).

## Out of scope

- Webhooks.
- Deletes (task 05).

## Acceptance criteria

- Unit tests: unchanged parent + changed grandchild → only grandchild
  re-fetched/re-written; unchanged tree → no block fetches; old state
  file without children loads and triggers fetch; child removed from a
  changed parent drops from cache.
- E2E (procedure in task 02): second run on the scratch vault makes far
  fewer API calls than the first (log or count them) and rewrites no
  files; record both numbers in the PR description.

## Dependencies

Blocked by 03.
