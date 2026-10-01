# Bound Notion API requests with an HTTP timeout

## Problem

`notion.NewClient` uses `http.DefaultClient`, which has no timeout. A
stalled connection (seen repeatedly during E2E runs after a DNS drop)
hangs a sync for 15+ minutes inside a single search or block request.

## Scope

- Give the Notion API client an `http.Client` with a request timeout
  (e.g. 60s).
- Treat a timeout like other transient errors: retry with the existing
  rate limiter/backoff where retries already exist; otherwise surface
  the error so the failing root is reported and the run continues.
- Attachment downloads keep their own client and 5-minute timeout.

## Out of scope

- Configurable timeouts.

## Acceptance criteria

- Unit test with an `httptest` server that stalls: the request fails
  within the timeout instead of hanging.
- Existing tests pass; no change to successful-request behaviour.

## Dependencies

None.
