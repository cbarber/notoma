# Discover child pages and inline databases nested inside blocks

## Problem

`extractChildPages` (`cmd/notoma/sync.go`) only scans a page's top-level
blocks for `ChildPageBlock`. Child pages nested inside `column_list` →
`column`, toggles, toggleable headings, callouts, quotes, list items or
synced blocks are never synced, nor is anything below them. Inline
`child_database` blocks are never synced at all unless the database is a
workspace root.

Measured against the Obsidian importer on the Mirza workspace: notoma
tracks 878 pages, Obsidian found 9,146. Example: "The Mirza Engineering
Handbook" (`3c0096f0-3c9b-8121-abca-ec34a6f22932`) lives at
`Engineering (8583460b…) → column_list → column → child_page`. The
Engineering page also has an inline `child_database` "Engineering Digest".

## Scope

- Collect child pages **and** child databases from the full block tree of
  a page: descend into any block with `has_children: true`, except
  `child_page`/`child_database` themselves (those are the children).
- Reuse the nested blocks the transformer already fetches if practical,
  instead of fetching them twice. If that's invasive, a separate fetch is
  acceptable, but no block's children may be fetched twice within one
  page's processing.
- Sync discovered child databases with the existing `syncDatabase` path.
- Database entry bodies must get the same treatment: child pages and
  databases nested in a database row's content are synced too.
- Keep the existing `visited` cycle protection; a database reached both
  as a workspace root and as an inline child is synced once per run.
- Synced blocks (`synced_block` with `synced_from`) pointing at another
  page: descend into the original's children as the transformer does.

## Out of scope

- Filename collisions and folder layout (task 03).
- Incremental skipping behaviour (task 05) — keep current `NeedsSync`
  semantics unchanged.
- Linked database views (`child_database` that references another
  database without access) — log at debug and skip on API 404/403.

## Acceptance criteria

- Unit tests: child pages/databases found inside column_list/column,
  toggle, toggleable heading, callout, bulleted list item, synced block;
  nested 3+ levels; mixed with top-level children; ordering stable.
- Unit test: inline child database inside a page triggers a database sync.
- E2E (see "Verification" below): `The Mirza Engineering Handbook.md` and
  the `Engineering Digest` database folder exist in the scratch vault.
- README unchanged unless behaviour visible to users changed.

## Verification

Run a real sync into a scratch vault — never the user's vault:

```bash
cp /Users/cbarber/src/notoma/.env .
cat > <scratch>/e2e.yaml <<EOF
sync: {discover_workspace_roots: true}
output: {vault_path: "<scratch>/vault", attachment_folder: "_attachments"}
state: {file: "<scratch>/state.json"}
options: {download_attachments: false}
EOF
go run ./cmd/notoma sync --config <that file> --quiet
```

Never set `update_notion_timestamp` — sync must stay read-only to Notion.
