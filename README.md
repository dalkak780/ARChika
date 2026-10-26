# ARChika

[![CI](https://github.com/dalkak780/ARChika/actions/workflows/ci.yml/badge.svg)](https://github.com/dalkak780/ARChika/actions/workflows/ci.yml) [![Docker](https://github.com/dalkak780/ARChika/actions/workflows/docker.yml/badge.svg)](https://github.com/dalkak780/ARChika/actions/workflows/docker.yml)

ARChika is a self-hosted gallery and reader for comic and image archives. A single Go binary serves the API and an embedded React UI, and keeps its catalog in SQLite. You don't need Node.js, Redis or a separate database in production.

It reads ZIP/CBZ, RAR/CBR, 7z/CB7 and plain image folders in place and never modifies your files. ARChika starts with its own fresh catalog; it does not import a LANraragi database or offer a LANraragi-compatible API.

## Quick start with Docker

Create the data, cache and plugin directories as the user that will run the service (not root):

```sh
cp archika.example.yaml archika.yaml
mkdir -p data cache plugins
export PUID="$(id -u)" PGID="$(id -g)"
export LIBRARY_PATH=/absolute/path/to/library
export DATA_PATH="$PWD/data" CACHE_PATH="$PWD/cache"
docker compose build
```

Start the service and read the initial administrator password:

```sh
docker compose up -d
docker compose logs archika
```

Server logs go to stderr. Set `ARCHIKA_LOG_LEVEL` to `debug`, `info` (default), `warn`, or `error`, and `ARCHIKA_LOG_FORMAT` to `text` (default) or `json`. Invalid values stop startup. Logs are for administrators; request queries and credentials are omitted.

On first start, if no accounts exist, ARChika creates `admin` and prints a one-time password to the logs. You must change it at first sign-in. The password is not shown again.

To supply your own password, set `ARCHIKA_INITIAL_ADMIN_PASSWORD_FILE` to a mounted Docker secret file, or set `ARCHIKA_INITIAL_ADMIN_PASSWORD`. The file takes precedence; one trailing newline is removed. Passwords must be 12–1024 bytes. Set `ARCHIKA_INITIAL_ADMIN_NAME` to choose a name other than `admin`. A supplied password does not require a change at first sign-in. These settings apply only when no accounts exist.

Optionally, create the administrator via stdin before the first start:

```sh
read -rs ARCHIKA_INITIAL_PASSWORD
printf '%s\n' "$ARCHIKA_INITIAL_PASSWORD" | docker compose run --rm -T archika admin --name admin
unset ARCHIKA_INITIAL_PASSWORD
```

Then open the UI and run **Administration → Jobs → Discover / scan**. ARChika never scans on its own at startup or when you add a root, so the library stays empty until you run this.

### Prebuilt images

Multi-arch images (`linux/amd64`, `linux/arm64`) are published to `ghcr.io/dalkak780/archika`:

- `main`: latest commit on the default branch
- `latest`, `X.Y.Z`, `X.Y`: release tags

To use one, set `image: ghcr.io/dalkak780/archika:latest` in `compose.yaml`, remove `build: .`, and run `docker compose pull` instead of `docker compose build`.

### What the compose file sets up

- The HTTP port is bound to `127.0.0.1`. Put a reverse proxy with HTTPS in front, or change the binding if the network is trusted.
- The container runs read-only with all capabilities dropped and `no-new-privileges`.
- The library and plugin directories are mounted read-only. Data, cache and `/tmp` are separate writable mounts.
- To add more libraries, add another read-only mount and a matching entry in `allowed_paths`.

Keep the SQLite data directory on a local filesystem; WAL mode doesn't work reliably on network mounts. Don't share a cache directory between two instances (a directory lock refuses to start the second one). ARChika never runs `chown` or `chmod` on existing directories, so set ownership yourself.

`GET /healthz` returns as soon as the HTTP server is ready. It doesn't wait for background jobs or check library files.

## Building from source

Requirements: Go 1.25.1+, Node.js 24.11+ and pnpm 12. Linux is the production target; macOS works for development.

The SPA uses TanStack Redact 0.1.5 through its Vite plugin, retaining React imports and the React Compiler. React packages remain installed for types and dependency compatibility. Redact renders synchronously rather than using React's concurrent scheduler. The version is pinned to respect the dependency release-age policy.

```sh
make build
cp archika.example.yaml archika.yaml
read -rs ARCHIKA_INITIAL_PASSWORD
printf '%s\n' "$ARCHIKA_INITIAL_PASSWORD" | bin/archika admin --config archika.yaml --name admin
unset ARCHIKA_INITIAL_PASSWORD
bin/archika serve --config archika.yaml
```

A plain `go build` uses a pure-Go image backend. AVIF and JXL pages are still served as-is, but resizing or converting them needs the libvips build used by the container image (`-tags vips`).

## Configuration

Configuration is JSON or YAML; see [`archika.example.yaml`](archika.example.yaml). Environment variables are not expanded inside the file.

Byte-size settings accept plain byte counts or strings such as "64MiB" or "1.5GB"; KiB/MiB/GiB/TiB and K/M/G/T are 1024-based, while KB/MB/GB/TB are 1000-based.

`allowed_paths` is the set of directories an administrator may register as library roots. The UI can't register paths outside it.

For HTTPS deployments, set `secure_cookies: true` and make sure the reverse proxy forwards the original `Host` header.

### Accounts

The UI supports English and Korean, follows your browser by default, and can be changed in Account.

Administrators create accounts, disable them, reset passwords and grant access to individual roots from the UI. New non-admin accounts can't see any root until they are granted one. Passwords are hashed with Argon2id. Sessions are stored server-side and use HttpOnly, SameSite cookies with CSRF tokens.

## Library and indexing

Choose Folders to browse an accessible root's directory tree. Folder cards count works throughout each subtree; the work list, search, favorites, and reader navigation stay within the current folder. `GET /api/folders?root=<rootID>&path=<relativePath>` returns `root_id`, `root_name`, `path`, the direct `works` count, and `folders` with `name`, `path`, recursive `works`, and `cover_work_id`. `GET /api/works` and `GET /api/works/{id}/neighbors` accept `folder=<relativePath>` with a required `root`; an empty path selects the root's direct works. Omitting `folder` keeps the full-root search.

- Each archive file is one work. A folder that contains images and no subfolders is a gallery. Other folders are only used for organization.
- Titles are the original file or folder name without the archive extension. An optional per-root RE2 pattern can extract tags (artist, group, event, series, language, ...) from filenames; it never changes the title.
- Discovery only records paths and file metadata. Page lists are built later in the background, or immediately when you open a work that hasn't been indexed yet. Thumbnails are generated on first view.
- Work IDs, page lists and cover choices survive restarts. Where the filesystem provides a reliable file identity, hard links and bind-mounted copies of the same file are tracked as one work with several paths.
- Symlinks are followed only when they stay inside the same root.
- Missing files stay in the catalog but are hidden from browsing. Administrators can review and clean up offline records; cleanup never deletes files on disk. If a mountpoint suddenly looks empty, ARChika probes a few known files and can mark the root unavailable instead of treating every work as deleted.
- When you change a root's path, ARChika checks a sample of files at the new location before switching. If the check fails, the change waits for your confirmation.

Filesystem watching is opt-in per root. Watches are installed only on directories found by a previous scan. If the OS watch limit is reached, the watcher overflows, the library is on a network filesystem, or the service was down, run a scan to catch up.

## Search

The catalog uses SQLite FTS5 with `unicode61` and `trigram` indexes. Search queries only the database; it never opens archives or walks the filesystem.

```text
artist:someone AND (language:korean OR language:japanese)
path:작가 NOT title:credits
root:ROOT_ID AND substring:archive
word:collection OR tag:series:example
favorite:true
```

- Operator precedence is `NOT`, then `AND`, then `OR`. Terms next to each other are combined with `AND`. Use quotes for phrases or to search for a literal `AND`/`OR`/`NOT`.
- Terms of three or more characters match anywhere in a word. One- and two-character terms match the start of words.
- Field prefixes: `word:`, `prefix:`, `exact:`, `title:`, `path:`, `root:`, `substring:`, and namespaced tags such as `artist:` or `tag:series:`.
- There is no CJK morphological analysis or transliteration.

Results can be browsed with page numbers or infinite scroll. The default order is newest first; you can also sort by title (natural order) or file modification time. Page numbers above 10000 are rejected; use scrolling instead. Saved searches and favorites are per account.

## Reader

- Single page, portrait spreads, or continuous scroll, with viewport, width, height and original-size fit modes.
- The navigation bar has first, previous, next and last page buttons. Click the page indicator or press G, then Enter, to jump. File name, known dimensions and cached image size appear beside it.
- Open the thumbnail overview with Q to jump to a page, use it as the cover or reset the cover. Options (O) apply immediately and save automatically, including reading direction, resize, preload count and auto-next interval. Zoom stays local to the session; Open original is in Options.
- Right-to-left reading reverses paged arrows, tap zones, swipes and spread order. Continuous scrolling is unchanged.
- Auto-next (N) shows a countdown, pauses while a panel is open or the tab is hidden, and continues into the next work when available. Previous/next work buttons and comma/period follow the library search and keep its filters when you return. Manual page turns stop at the ends.
- Arrow keys or A/D turn pages; Space/Shift+Space move forward/back. Home/End or Shift+arrows jump to the ends. F toggles fullscreen, P single/spread, M direction, and Backspace returns to the library. The help panel (?) lists shortcuts and touch zones. Shortcuts ignore text entry; Escape closes panels or cancels a page jump.
- Pages are sent as the original bytes when the browser can display them, so animated images keep their animation. When the browser can't decode AVIF, WebP or JXL, ARChika sends a PNG instead, and marks it as a still frame if the original was animated.
- Fullscreen hides the reader controls automatically; tap the centre of the page or the controls button to show them again. Browser-level fullscreen (such as F11) is detected too. Where the Fullscreen API is unavailable or declined (for example on iPhone), the reader switches to an in-page fullscreen view instead: Back leaves it on the same page, and Back again returns to the library with your filters and scroll position.
- ARChika doesn't keep a reading history.

Archive formats are handled behind the [`archive.Adapter`](internal/archive/archive.go) interface. ZIP, RAR and 7z are read by a small pool of helper processes, and the next pages are read ahead based on how fast you're reading.

### Resource defaults

All of these can be changed in the config file.

| Setting | Default |
|---|---:|
| In-memory page cache | 64 MiB |
| Disk page cache | 1 GiB |
| Maximum page size | 64 MiB |
| Archive decompression workers | 2 |
| Open archive sessions | workers + 2 |
| Concurrent image transforms | 1 |
| Maximum pixels per transform | 64 million |
| Pages read ahead | up to 12 (adaptive) |
| Read-ahead bytes per page turn | up to 16 MiB |
| Thumbnail size | 320 × 480 |
| Maximum entries per archive | 100000 |
| Helper time limit per operation | 30 s |

Thumbnails are stored permanently and regenerated only when the source or cover changes. A thumbnail that can't be generated (for example, an unsupported format) is marked as failed until the file changes or an administrator retries it. The disk page cache is temporary and is cleaned up in the background.

On Linux, each archive or image helper is limited to 768 MiB of data, 2 GiB of address space and 120 CPU seconds. Total memory use is higher than the cache size: SQLite, active responses and decoders also use memory, so set a container memory limit accordingly. On macOS, helpers run in separate processes with timeouts but without the memory cap.

## Background jobs

Scans, indexing, metadata fetching and thumbnail warm-up run as jobs that you can pause, resume, retry and cancel from the UI. Each job shows its progress and failed items.

After a restart, small incremental jobs resume automatically. Large jobs (full scans, bulk indexing, metadata backfill, re-parsing filenames, thumbnail warm-up) stay paused until you resume them. Pausing all jobs globally doesn't affect the web UI.

## Metadata

Built-in providers: E-Hentai/ExHentai, nHentai (API v2), Chaika.moe and local JSON files. Matching follows the logic of the LANraragi and Ougi plugins.

- A match on title alone is shown as a candidate for you to confirm. Matches on a source URL or provider ID are linked automatically.
- Manual edits always win, then providers in your configured priority order, then values from the filename. Fetching metadata never changes the displayed title.
- A failed fetch keeps the previous metadata. Up to three past results per work and provider are kept, with sensitive fields removed.

Automatic metadata fetching is off by default. When you enable it for a root, it runs only for works added from then on, not for the existing library.

### Network

By default, provider hostnames are resolved through Cloudflare DNS over HTTPS. You can configure another DNS-JSON endpoint or use `dns_mode: system`. ARChika never falls back to system DNS silently. `proxy_url` accepts an HTTP(S) CONNECT proxy. Requests only go to public IP addresses, redirects are checked, and credentials are not sent across origins.

### Provider credentials

Credentials are encrypted with AES-GCM using a key that you keep outside the database:

```sh
bin/archika keygen
# Pass the value as ARCHIKA_MASTER_KEY, or through your secret manager.
```

Enter credentials as JSON in the admin UI, for example `{"cookie":"..."}` or `{"token":"..."}`. The API never returns them. Back up the master key separately; if you lose it, you have to enter the credentials again.

### Local JSON

```json
{"fields":{"title":"Searchable remote title","description":"..."},"tags":["artist:example"],"source_id":"local-record"}
```

Import a file for one work from the Metadata page, or let the local provider read `archive.zip.json` or `gallery/metadata.json` next to the work. The local provider never uses the network.

## Plugins

Plugins are executables that an administrator places in `plugin_directory` and explicitly trusts in the UI. Each lookup starts a new process, which exits when the lookup is done.

> **Plugins are not sandboxed.** A plugin runs with the same filesystem and network access as ARChika. Only install plugins you trust, or isolate them with OS or container restrictions.

The manifest declares `id`, `name`, `version`, `protocol_version: 1`, `capabilities: ["metadata"]`, the exact `hosts` it may contact, requested `permissions`, and a `config_schema`. The schema supports object properties of type string, boolean, number and integer, with `required`, `enum`, numeric bounds and string length. Other JSON Schema keywords are rejected. Protocol v1 only runs the `metadata` capability.

### Protocol v1

Plugins speak newline-delimited JSON-RPC 2.0 over stdin/stdout, with messages up to 1 MiB.

1. The host sends `initialize` (id 1) with the protocol version and capabilities.
2. The host sends `provider.lookup` (id 2) with `input`, the validated `config`, and only this plugin's `credentials`.
3. The plugin returns an array of results: `source_id`, `source_url`, `fields`, `tags`, `confidence`, `evidence`, `version`, and optionally `raw`.

While a lookup is running, the plugin can ask the host to make HTTP requests with `host.http` (`method` GET or POST, `url`, `headers`, `body_base64`). The host checks each request against the manifest's `hosts` and returns `body_base64`, or a JSON-RPC error. On cancellation the host sends `$/cancelRequest`, then kills the process group.

On Linux, plugins get data-size and CPU limits. Keep the container's memory, PID and tmpfs limits in place as well.

## Development

```sh
make test   # Go tests, frontend check (oxfmt, oxlint, TypeScript) and tests
make race   # Go tests with the race detector

# Frontend only (web/ uses Vite+; vp wraps Vite, Vitest, Oxlint and Oxfmt)
pnpm --dir web dev     # dev server, proxies /api to 127.0.0.1:8080
pnpm --dir web check   # format, lint and type check
pnpm --dir web exec playwright install chromium
pnpm --dir web run test:e2e  # builds assets, starts an isolated Go server, runs Chromium, cleans up

# Optional, slower checks
ARCHIKA_SCALE=100000 go test ./internal/app -run TestSearchScale -v -count=1
ARCHIKA_NETWORK=1 go test ./internal/app -run TestDoHAndPinnedProxySmoke -v -count=1
go test ./internal/archive -run '^$' -bench Navigation -benchmem
```

The E2E check covers authentication, explicit discovery, search/favorites, folder navigation, reader spreads/overview/immersive Back, persisted language changes, mobile continuous virtualization and logout. It uses generated image galleries and a temporary database, not an existing library. Set `ARCHIKA_E2E_IMAGE=archika:local` to exercise a built Docker image with a read-only root filesystem and library, non-root UID and dropped capabilities. `ARCHIKA_E2E_LAYOUT` and `ARCHIKA_E2E_SCREENSHOT` optionally save rendered-layout JSON and a mobile screenshot. Chromium checks do not certify physical mobile gestures, other browser engines or every administration/provider workflow.

`docker build --target verification -t archika:verify .` builds an image with extra diagnostic and fixture-generation tools. The regular runtime image doesn't include them.

## License

ARChika is licensed under [Apache-2.0](LICENSE); see [NOTICE](NOTICE). Third-party components keep their own licenses, listed in [`THIRD_PARTY_NOTICES.txt`](THIRD_PARTY_NOTICES.txt).
