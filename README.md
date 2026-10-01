# notoma

One-way sync tool from Notion to Obsidian via Notion API. Notoma is designed for regular ongoing incremental updates. If you need a one-time update, Obsidian-native Notion importer will work faster.

## Features

- Sync Notion pages and databases to Obsidian-flavored markdown
- Incremental updates — only sync pages modified since last run
- Database → Obsidian Bases (`.base` files) conversion
- Attachment handling with automatic download
- Rate limiting to respect Notion API limits

## Prerequisites

- Go 1.24+ (for local development)
- A Notion integration token ([create one here](https://www.notion.so/my-integrations))

## Building Locally

```bash
# Clone the repository
git clone https://github.com/natikgadzhi/notoma.git
cd notoma

# Build using Make (outputs to build/)
make build

# Verify it works
./build/notoma --help
```

Or build manually:

```bash
go mod download
go build -o build/notoma ./cmd/notoma
```

## Building with Docker

```bash
make docker

# Or manually:
docker build -t notoma .

# Run it
docker run --rm notoma --help
```

## Make Targets

```bash
make build   # Build binary to build/
make test    # Run tests
make clean   # Remove build artifacts
```

## Configuration

### 1. Set up your Notion token

Create a `.env` file (or set environment variables):

```bash
cp .env.sample .env
# Edit .env and add your NOTION_TOKEN
```

### 2. Create a config file

Create `config.yaml`:

```yaml
sync:
  roots:
    - url: "https://www.notion.so/myworkspace/Personal-Wiki-abc123..."
      name: "Personal Wiki"
    - url: "https://www.notion.so/myworkspace/def456...?v=xyz789..."
      name: "Reading List"

output:
  vault_path: "/path/to/obsidian-vault"
  attachment_folder: "_attachments"

state:
  file: "/path/to/notoma-state.json"

options:
  download_attachments: true
```


## Usage

```bash
# Full sync
./notoma sync --config config.yaml

# Preview changes without writing files
./notoma sync --config config.yaml --dry-run

# Force full resync (ignore state)
./notoma sync --config config.yaml --force

# Show version
./notoma version
```

### With Docker

```bash
docker run --rm \
  -e NOTION_TOKEN="your-token" \
  -v $(pwd)/config.yaml:/config.yaml:ro \
  -v /path/to/vault:/vault \
  notoma sync --config /config.yaml
```

## Output Layout

Pages are written flat, as `Title.md`. Database entries go in a folder named after the database, next to its `.base` file. Each synced page and database entry starts with frontmatter recording its Notion ID, using the same key as Obsidian's Notion importer:

```yaml
---
notion-id: 3c0096f0-3c9b-8121-abca-ec34a6f22932
---
```

Names that differ only in case count as the same name. When two pages, entries or databases would get the same path:

- The one already at that path in the sync state keeps it. With no earlier owner, the first one synced keeps it. Configured roots sync in config order and discovered roots in ID order, so the outcome is deterministic.
- The other gets the last 8 hex characters of its ID as a suffix, as in `Engineering (c27d1c3a).md`. If that name is taken too, the full ID is used.
- A suffixed file keeps its name on later runs, even if the bare name frees up.
- Links to child pages point at the suffixed name.

When a page is renamed in Notion, its file is moved to the new name. Notoma deletes the old file only if that file's `notion-id` matches the page.

**Upgrading from an earlier version:** run `notoma sync --force` once. Files written by earlier versions have no `notion-id` frontmatter (pages) or use the older `notion_id` key (database entries). Until a file is rewritten, a renamed page leaves its old file behind.

## Development

```bash
# Run tests
go test ./...

# Run tests with race detection
go test -race ./...

# Format code
go fmt ./...

# Run linter (requires golangci-lint)
golangci-lint run
```

## Contributing

This project uses a task-driven multi-agent development workflow. Tasks are tracked as markdown files in the `tasks/` directory:

```
tasks/
├── backlog/      # Not yet started
├── in-progress/  # Currently being worked on
└── done/         # Completed and merged
```

All code changes go through pull requests. See `CLAUDE.md` for the full development workflow.

## License

MIT
