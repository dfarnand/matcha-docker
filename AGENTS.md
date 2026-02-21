# Matcha Docker Image

[Matcha](https://github.com/piqoni/matcha) generates markdown from RSS feeds, intended for daily news digests.

## Architecture

Single container with cron-scheduled Matcha runs + Go webapp for viewing markdown files.

## File Structure

```
.
├── Dockerfile              # Multi-stage: builds Matcha + Go webapp
├── docker-compose.yml      # Service definition with volumes and env
├── config/
│   └── config.yaml         # Matcha configuration (feeds, paths)
├── output/                 # Generated markdown digests (bind mount)
└── webapp/
    ├── app.go             # Go webapp (routes, markdown rendering)
    ├── go.mod             # Go module dependencies
    ├── go.sum
    ├── static/
    │   └── style.css      # Dark terminal theme
    └── entrypoint.sh      # Startup script (cron + webapp)
```

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MATCHA_VERSION` | GitHub release tag to build | `v0.9.0` |
| `CRON_SCHEDULE` | Cron expression for Matcha runs | `0 6 * * *` |

## Volumes

| Host | Container | Purpose |
|------|-----------|---------|
| `./config` | `/app/config` | Matcha config files |
| `./output` | `/app/output` | Generated markdown digests |

## Webapp Routes

| Route | Description |
|-------|-------------|
| `GET /` | Render most recent markdown file |
| `GET /files` | JSON list of available files (newest first) |
| `GET /file/<filename>` | Render specific markdown file |
| `GET /static/*` | Serve static assets (CSS) |

## Cron Configuration

- Configured in `/etc/crontabs/root` at startup via `entrypoint.sh`
- Runs: `/usr/local/bin/matcha -c /app/config/config.yaml`
- Verify with: `docker exec <container> cat /etc/crontabs/root`
- Cron daemon runs as: `crond -b -l 2`

## Build & Run

```bash
docker-compose up --build
```

Access at `http://localhost:8080`

## Dependencies

- Matcha: Built from source at `MATCHA_VERSION` tag
- Webapp: Uses `github.com/gomarkdown/markdown` for rendering

## Development Notes

- Webapp is built statically with `CGO_ENABLED=0` for minimal image size
- Markdown files are named by date (e.g., `2026-02-21.md`)
- File list is sorted newest-first in the sidebar
- Dark theme uses monospace fonts (Courier New) with terminal green accent (#4af626)
- Mobile: Sidebar collapses to hamburger menu below 600px width
