# Make output paths collision-safe and keyed by Notion ID

## Problem

Pages are written flat into the vault root (or a database folder) as
`SanitizeFilename(title).md`. Two pages with the same title map to the
same file and the last one written wins. The Mirza state has 9 such
collisions, e.g. two workspace-root pages titled "Engineering"
(`8583460b…` hub and `68f93ef0…`) both claim `Engineering.md`. When a
page is renamed or moved in Notion, the old file is left behind as a
stale duplicate.

## Scope

- Every synced page and database entry gets `notion-id: <uuid>` in YAML
  frontmatter (database entries already have frontmatter — add the key;
  standalone pages get a minimal frontmatter block). Match the key name
  the Obsidian importer uses: `notion-id`.
- Deterministic path allocation per run: when two resources resolve to
  the same path, the one already owning that path in state keeps it; the
  other gets `Title (abcd1234).md` using the first 8 hex chars of its ID.
  With no prior owner, the lexicographically smaller ID keeps the bare
  name. Comparison is case-insensitive (macOS/APFS default).
- Same rule for database folders.
- Wiki links (`[[Title]]`) emitted for child pages must point at the
  allocated filename (without `.md`) so links resolve to the right file.
  If link targets are resolved at transform time, a lookup from page ID
  to allocated name is needed; pages not yet allocated fall back to the
  sanitized title.
- Move/rename: if a resource's newly allocated path differs from
  `LocalPath` in state, remove the old file (only if it exists and its
  frontmatter `notion-id` matches) and update state.

## Out of scope

- Nested folder hierarchy — keep the flat layout.
- Deleting files for resources removed from Notion (task 05).

## Acceptance criteria

- Unit tests for allocation: no collision; collision with and without
  prior state owner; case-only collision; three-way collision; stable
  across runs.
- Unit test: rename in Notion removes old file and writes new one; a
  file at the old path whose `notion-id` differs is never deleted.
- E2E against a scratch vault (procedure in task 02): both "Engineering"
  pages exist as separate files, `Engineering.md` is the one that owned
  it in state (or smaller ID on a fresh state), and each file's
  frontmatter `notion-id` is correct.
- README documents the `notion-id` frontmatter and suffix rule.

## Dependencies

Blocked by 02 (touches the same recursion code).
