# Checkpoint sync state during long runs and on interrupt

## Problem

State is saved only once, at the end of `runSync`. A full sync of a
9k-page workspace takes a long time; a crash, rate-limit failure or
Ctrl-C loses all progress and the next run starts from scratch.

## Scope

- Save state every N synced resources (pick a sensible N, e.g. 100) and
  when the context is cancelled by SIGINT/SIGTERM (`setupSignalHandler`
  in `cmd/notoma/cli.go`).
- `SaveState` must write atomically (temp file in the same dir + rename)
  so an interrupted save can't corrupt the file. Check whether it already
  does.
- Downloaded attachments recorded so far must be written before their
  state is checkpointed, or not recorded until written — state must
  never claim an attachment exists that isn't on disk.
- No checkpointing in `--dry-run`.
- If any goroutines touch state concurrently (worker pool), state access
  must be safe; run `go test -race ./...`.

## Out of scope

- Resuming mid-page; granularity is per resource.

## Acceptance criteria

- Unit tests: checkpoint after N resources; atomic write leaves the
  previous file intact if writing fails; cancelled context triggers a
  save; dry-run never writes.
- E2E (procedure in task 02): start a sync on a fresh scratch state,
  send SIGINT after ~30s, confirm the state file exists and is valid
  JSON with >0 resources, and a rerun skips them.

## Dependencies

None. May run in parallel with 02 — keep changes in `runSync` and
`internal/sync` to minimise conflicts in `cmd/notoma/sync.go`.
