# Matcha Docker Image

[Matcha](https://github.com/piqoni/matcha) generates markdown from RSS feeds, intended for daily news digests.

## Architecture

Single container running a Go webapp that:
- serves the generated markdown digests (public),
- hosts a password-protected settings UI (`/settings`),
- **schedules and runs Matcha itself** — there is no cron daemon,
- **generates Matcha's `config.yaml` from stored settings before every run**.

`settings.json` is the source of truth. `config.yaml` is a generated artifact.

```
settings.json ──► RunMatcha ──► config.yaml ──► matcha -c config.yaml ──► /app/output/*.md
```

## File Structure

```
.
├── Dockerfile              # 3 stages: matcha (go1.21), webapp (go1.25), alpine runtime
├── docker-compose.yml
├── output/                 # Generated markdown digests (bind mount)
├── config/                 # State + generated config (bind mount, gitignored)
└── webapp/
    ├── main.go            # Flags, startup, mux, middleware wiring, settings/run API
    ├── app.go             # Digest rendering only (list, render, filename sanitizing)
    ├── settings.go        # Settings types, store, validation, container paths
    ├── migrate.go         # One-time config.yaml -> settings.json import
    ├── config_gen.go      # Settings -> config.yaml (yaml.v3), pinned paths
    ├── auth.go            # bcrypt, sessions, CSRF, rate limiting, handlers
    ├── scheduler.go       # Cron parsing and the in-process run loop
    ├── runner.go          # flock, matcha execution, last-run record
    ├── feedpreview.go     # RSS/Atom title extraction for the preview popover
    ├── templates.go       # //go:embed template loading
    ├── httputil.go        # JSON helpers
    ├── atomicfile.go      # writeFileAtomic
    ├── *_test.go
    ├── templates/         # layout.html, index.html, login.html, settings.html
    ├── static/            # style.css, gruvbox-palette.css, settings.css,
    │                      # settings.js, sw.js, manifest.json, icons/
    ├── entrypoint.sh      # exec webapp
    └── matcha-runner.sh   # webapp -run -source manual-cli
```

## Storage (all in `/app/config`)

| File | Mode | Contents |
|------|------|----------|
| `settings.json` | 0600 | Source of truth. Holds `openai_api_key`. |
| `auth.json` | 0600 | bcrypt hash, session secret, password version. |
| `initial-password.txt` | 0600 | Generated password; removed on password change. |
| `config.yaml` | 0600 | **Generated.** Never hand-edit. |
| `config.yaml.pre-webapp.bak` | 0600 | User's original, kept once at migration. |
| `last-run.json` / `last-run.log` | 0644 | Status and output of the last run. |
| `.matcha-run.lock` | 0600 | `flock` target for cross-process run exclusion. |

Failure behaviour, both deliberate:
- Corrupt `settings.json` → moved to `settings.json.corrupt-<unix>`, defaults served, **saves disabled** until the user discards it. Never silently overwritten; it may hold dozens of feeds.
- Corrupt `auth.json` → **fail closed**, settings area returns 503. Never regenerates credentials, or corruption becomes a free takeover. Digest rendering keeps working either way.

## CLI modes

| Command | Effect |
|---------|--------|
| `webapp` | Run the server (default). |
| `webapp -run -source <s>` | Regenerate config, run matcha once, record the result, exit with matcha's status. |
| `webapp -generate-config [-o path]` | Print/write the generated config. Debugging aid. |

`matcha-runner` shells into `webapp -run`, so a manual `docker exec` run shares the scheduler's code path and gets recorded the same way. **Never** make `RunMatcha` invoke `matcha-runner` — it would recurse.

## Environment Variables

| Variable | Description | Default |
|----------|-------------|---------|
| `MATCHA_VERSION` | Build arg: matcha release tag | `v0.9.0` |
| `CRON_SCHEDULE` | Seeds `settings.schedule` on first start **only** | `0 6 * * *` |
| `TZ` | Timezone the schedule resolves in | container default |
| `PORT` | Listen port | `7321` |
| `MATCHA_COOKIE_SECURE` | Force the `Secure` cookie flag | unset |
| `TRUST_PROXY` | Honour `X-Forwarded-For` for rate limiting | unset |
| `MATCHA_CONFIG_DIR` / `MATCHA_OUTPUT_DIR` / `MATCHA_STATIC_DIR` / `MATCHA_BINARY` | Path overrides for running outside the image | container paths |

## Webapp Routes

| Route | Method | Auth | Description |
|-------|--------|------|-------------|
| `/` | GET | public | Most recent digest |
| `/file/{name}` | GET | public | Specific digest (filename sanitized) |
| `/files` | GET | public | JSON list, newest first |
| `/static/*` | GET | public | Static assets |
| `/login` | GET, POST | public | Sign in |
| `/logout` | POST | session + CSRF | Sign out |
| `/settings` | GET | session | Settings page |
| `/api/settings` | GET, POST | session (+CSRF) | Read / save the whole settings document |
| `/api/settings/discard` | POST | session + CSRF | Discard an unreadable settings file |
| `/api/config-preview` | GET | session | Generated YAML (`?reveal=1` unmasks the key) |
| `/api/feed-preview` | POST | session + CSRF | Fetch a feed, return item titles |
| `/api/run` | POST | session + CSRF | Start a run (202, or 409 if busy) |
| `/api/run/status` | GET | session | Running state, next run, last result |
| `/api/password` | POST | session + CSRF | Change password |

## Rules that are easy to get wrong

- **The settings UI must never use `class="content"`.** `style.css` clamps `.content img` to 16x16 `!important` and makes `.content p a` / `.content li a` `display:block`. Use `.st-page` and the `st-` namespace.
- **Bump `CACHE_NAME` in `sw.js`** whenever any file in `STATIC_ASSETS` changes. `/static/*` is cache-first, so users otherwise run stale JS against a new API.
- **`sw.js` must keep bailing** on `/settings`, `/login`, `/logout`, `/api/*` and non-GET requests. Without it the navigation branch caches an authenticated page and serves the digest under `/settings` when offline.
- **Keep `<input class="menu-toggle">` immediately before `<nav class="sidebar">`** in `layout.html`. The mobile menu is a `~` sibling selector.
- **Tokens rendered into HTML must use `base64.RawURLEncoding`** (`generateToken`), not `StdEncoding`. `+` becomes `&#43;` under HTML escaping, and the resulting `;` makes `ParseForm` reject the submission.
- **Generate YAML with `yaml.Marshal`, never string concatenation.** The prompt fields are free text containing colons, quotes and newlines.
- **Feed URLs must not contain whitespace.** Matcha splits on space and `log.Fatalf`s if the tail is not an integer, killing the whole run. Validated in `ValidateSettings`.
- `analyst_feeds` has no per-feed limit (matcha hardcodes 20); emitting a ` N` suffix there would corrupt the URL.
- `google_news_keywords` is a single comma-separated **string** in matcha, not a list.

## Scheduling

`scheduler.go` parses the cron expression with `robfig/cron/v3` and sleeps until `Next()`. Saving a new schedule signals a channel, so changes apply immediately with no restart.

Consequence: **scheduled digests depend on the webapp process being alive.** It is PID 1 under `restart: unless-stopped`, so a crash restarts the container rather than silently stopping digests.

## Dependencies

- Matcha: built from source at `MATCHA_VERSION` (stage 1, `golang:1.21-alpine`)
- Webapp (stage 2, `golang:1.25-alpine`, `GOTOOLCHAIN=local`):
  - `github.com/gomarkdown/markdown` — digest rendering
  - `gopkg.in/yaml.v3` — config generation and legacy import
  - `golang.org/x/crypto` — bcrypt (needs Go 1.25; this is why the stage is newer than stage 1)
  - `github.com/robfig/cron/v3` — schedule parsing and next-run computation

Feed preview uses `encoding/xml` rather than `gofeed` on purpose — five extra modules to display ten titles is not worth it in a small image.

## Build & Run

```bash
docker-compose up --build
```

Access at `http://localhost:7321`. The generated admin password is in the container log and in `config/initial-password.txt`.

### Running the webapp outside Docker

```bash
cd webapp
MATCHA_CONFIG_DIR=/tmp/m/config MATCHA_OUTPUT_DIR=/tmp/m/output \
MATCHA_STATIC_DIR=./static MATCHA_BINARY=/bin/true PORT=17321 go run .
```

```bash
go test ./...
```

## Development Notes

- Webapp is built with `CGO_ENABLED=0` for a static binary
- Templates are `//go:embed`-ed, so `Dockerfile` needs its `COPY webapp/templates` line
- Markdown files are named by date (e.g. `2026-02-21.md`), sorted newest-first
- Theme is Gruvbox Dark Hard (`gruvbox-palette.css` → semantic vars in `style.css`), Fira Code monospace
- Mobile: sidebar collapses to a hamburger below 600px
