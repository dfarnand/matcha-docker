# Matcha RSS Digest and Reader in Docker

This is a single docker container that includes [Matcha](https://github.com/piqoni/matcha) which is used to generate a daily digest of RSS feeds in markdown format. Packaged in with this is a simple webapp to read the markdown files and configure everything from the browser.

The web app has a similar goal as [go-digest](https://github.com/piqoni/go-digest). However that project is geared toward building a web site for github pages. I wanted to host a web app from a self-hosted container, which will only be available on my local network.

## Instructions

Docker compose:

```yaml
services:

  matcha:
    container_name: matcha
    image: ghcr.io/dfarnand/matcha-docker:latest
    ports:
      - 7321:7321
    volumes:
      - ${DOCKER_CONFIG_DIR}/matcha:/app/config
      - ${BACKEND_DOC_DIR}/matcha:/app/output
      - /etc/localtime:/etc/localtime:ro
    environment:
      - CRON_SCHEDULE=0 6 * * * # Only used the very first time; managed in the UI after that
    restart: unless-stopped
```

Start the container, then:

1. **Get the password.** On first start a random admin password is generated. It is printed in the container log (`docker logs matcha`) and also written to `initial-password.txt` in your config directory, so you can just read it off the host.
2. **Open `http://<host>:7321/settings`** and sign in.
3. **Change the password** in the Account section. This deletes `initial-password.txt`.
4. **Add your feeds**, set the schedule, and press **Run now** to generate a digest immediately.

That's it — there is no config file to create by hand.

### Locked out?

Delete `auth.json` (and `initial-password.txt`) from your config directory and restart the container. A new password is generated and logged.

## Configuration

Everything Matcha supports is configurable at `/settings`: feeds with per-feed item limits, summary and analyst feeds, Google News keywords, weather, reading time, images, Instapaper links, OpenAI settings, and notification webhooks. Feeds can be reordered, disabled without deleting them, and previewed (which fetches the feed and shows recent item titles) before you save.

The schedule is a standard 5-field cron expression, or a shortcut like `@daily`. It runs in the container's local time, so set `TZ` or mount `/etc/localtime` as above.

### config.yaml is generated

> **Do not edit `config.yaml`.** It is regenerated from your settings immediately before every run, so hand edits are lost.

Your settings live in `settings.json` in the config directory. `config.yaml` exists only so you can see exactly what Matcha is being given; the Advanced section of the settings page shows the same content.

`markdown_dir_path` and `database_file_path` are fixed to `/app/output/` and `/app/config/matcha.db`. They are not editable because they have to match the container's mounted volumes for digests and read history to survive a rebuild.

**Upgrading from an earlier version?** Your existing `config.yaml` is imported automatically on first start and kept as `config.yaml.pre-webapp.bak`. Anything that could not be imported is reported as a note at the top of the settings page.

### Files in the config directory

| File | What it is |
|------|-----------|
| `settings.json` | Your configuration. Contains your OpenAI key, so it is mode 0600. |
| `auth.json` | Password hash and session secret. Mode 0600. |
| `initial-password.txt` | The generated password. Deleted once you change it. |
| `config.yaml` | **Generated.** Regenerated before every run. |
| `config.yaml.pre-webapp.bak` | Your original hand-written config, kept once at import. |
| `last-run.json`, `last-run.log` | Status and output of the most recent run. |
| `matcha.db` | Matcha's history database, used to avoid repeating articles. |

### Environment variables

| Variable | Purpose |
|----------|---------|
| `CRON_SCHEDULE` | Seeds the schedule on first start only. Ignored afterwards. |
| `TZ` | Timezone the schedule runs in. |
| `MATCHA_COOKIE_SECURE` | Set to `1` only if a TLS proxy in front of the container does not send `X-Forwarded-Proto`. Setting it without HTTPS will break login. |
| `TRUST_PROXY` | Set to `1` to honour `X-Forwarded-For` for login rate limiting. Only behind a trusted proxy. |

## Running Matcha manually

```sh
docker exec matcha matcha-runner
```

This regenerates the config from your settings, runs Matcha, and records the result like any other run.

## Security

This is built for a home network. The digest pages stay public so the PWA and offline caching work as before; only the settings area requires a password.

**Do not expose port 7321 to the internet.** The container runs as root, and an authenticated user can trigger Matcha and make the server fetch arbitrary URLs (that is what the feed preview does — it deliberately allows LAN addresses so you can preview feeds from your own aggregator). If you need remote access, put it behind a VPN or an authenticating reverse proxy with TLS.

## Notes

- Thanks to [Edi Piqoni](https://piqoni.github.io/) for creating Matcha
- This repo was developed with LLM help. I probably won't be adding much as far as features, since its currently doing what I need, and I don't understand enough go or css to keep track of anything more complicated.
