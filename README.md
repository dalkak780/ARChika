# ARChika

A lightweight, self-hosted archive gallery and reader. One Go `net/http` process serves an embedded React/Vite SPA; production does not need Node.js, Redis, Perl, or an external database.

Implementation follows the latest decisions in [SPEC.md](SPEC.md), not superseded discussion paragraphs. This is a fresh SQLite catalog, not a LANraragi database/API migration.

## Build and first account

Requirements: Go 1.25.1 or newer, Node.js 24 and pnpm 12 for the frontend build. Linux is the production target; macOS supports local development. The container builds the existing govips/libvips implementation. An ordinary Go build uses the pure-Go image implementation: raw AVIF/JXL delivery still works, but AVIF/JXL transformations require the native container build.

```sh
make build
cp archika.example.yaml archika.yaml
```

Edit the example's paths for the host when running outside Docker. `allowed_paths` is the administrator-controlled registration boundary; the UI cannot register arbitrary paths or mount host directories. Configuration accepts JSON or YAML. There is no environment interpolation inside the configuration file.

Create the first administrator **before** starting the service. Supply the password on stdin rather than command-line arguments; there is no default password or public registration.

```sh
read -rs ARCHIKA_INITIAL_PASSWORD
printf '%s\n' "$ARCHIKA_INITIAL_PASSWORD" | bin/archika admin --config archika.yaml --name admin
unset ARCHIKA_INITIAL_PASSWORD
bin/archika serve --config archika.yaml
```

The setup command refuses an existing account database. Subsequent account creation, disabling, password reset and root grants are administrator operations in the UI. New non-admin accounts have no grants. Passwords use salted Argon2id; sessions are server-side, with HttpOnly/SameSite cookies and CSRF tokens. Use HTTPS at a reverse proxy and set `secure_cookies: true` for HTTPS deployments. The proxy must preserve the external Host header.

Open the Library, then **Administration → Jobs → Discover / scan**. Root registration and normal restart do not start discovery. An empty catalog before this explicit operation is intentional.

## Docker

Provision data/cache/plugin directories under the service UID/GID. Do not use root as the service identity, and do not recursively change ownership of existing libraries or caches. Keep SQLite data on a local filesystem suitable for SQLite WAL, not an unreliable network database mount.

```sh
cp archika.example.yaml archika.yaml
mkdir -p data cache plugins
export PUID="$(id -u)" PGID="$(id -g)"
export LIBRARY_PATH=/absolute/path/to/library
export DATA_PATH="$PWD/data" CACHE_PATH="$PWD/cache"
docker compose build
read -rs ARCHIKA_INITIAL_PASSWORD
printf '%s\n' "$ARCHIKA_INITIAL_PASSWORD" | docker compose run --rm -T archika admin --name admin
unset ARCHIKA_INITIAL_PASSWORD
docker compose up -d
```

Compose binds the HTTP port to loopback, drops capabilities, uses a read-only container filesystem, mounts originals and plugin executables read-only, and supplies separate persistent data, temporary page-cache and bounded `/tmp` storage. Configure the reverse proxy or intentionally adjust the port binding for a trusted network. Add additional read-only mounts and matching `allowed_paths` entries for additional roots.

The application creates only its own known data/cache directories and new files. It never runs recursive `chown`, `chmod`, permission walks or automatic repairs. An exclusive directory lock prevents two live services sharing a database or temporary cache generation.

`GET /healthz` reports HTTP readiness without checking every library file. Readiness does not wait for background jobs.

## Catalog, identity and indexing

- One ZIP/CBZ, RAR/CBR or 7z/CB7 is one work. An image-bearing leaf directory is a gallery. Parent directories organize works. Direct images in non-leaf folders are reported during explicit discovery rather than inventing nested galleries.
- Display titles preserve the original basename, removing only a recognized archive extension. The optional per-root RE2 filename parser produces tags, never replacement titles. LANraragi-derived fixture behavior covers creators/groups, events, series, language and trailing captures.
- Discovery records paths, lightweight observations and searchable names first. Separate stable-file and page-list tasks build manifests; neither stage extracts full images or creates thumbnails. Quiet periods are configurable. Clicking a stable, unindexed work promotes only that work.
- Persistent IDs, registration times, page manifests, provenance and cover selection survive restart. Reliable local filesystem identity shares a physical source across aliases; different physical copies are not merged. Birth identity is used where available. Unreliable filesystem identities fall back to separate records. A replaced hard-link alias can split from the still-live old physical source rather than blending two files.
- Aliases retain independent root authorization. Canonical names are selected by persistent root-ID order, then relative path, among the requesting user's accessible online aliases. Hidden aliases do not contribute filename/path/tag matches.
- Symlinks must stay inside the same registered root. Read-time traversal uses Go's descriptor-relative `os.Root`; originals are revalidated before cache delivery. Nested archives are non-page assets.
- Missing originals remain indexed, but disappear from normal browsing. Administrator views expose offline records and preview-before-cleanup. Cleanup rechecks absence and never deletes original files. A reachable empty mountpoint is not automatically treated as an intentionally deleted library: bounded known-source probes can mark a root unavailable and require mount verification. Restore the mount, or use the existing explicit reconciliation/force-approval controls after verifying it. Removing an intentionally retired root is a separate previewed operation.
- Root relocation uses a bounded sample. Inconclusive targets stay pending. Explicit force approval keeps IDs/indexes and records an audit warning; observed source mismatches trigger targeted reindexing, not a root sweep.

Watchers are opt-in per root. Explicit scans persist their directory/name worklists; they do not rely on filesystem enumeration order. Watch registration uses only configured roots and previously discovered directories, asynchronously after readiness. OS watch limits, overflow, network filesystems and downtime still require deliberate reconciliation. No watcher implementation can guarantee missed events did not occur. Failed directory traversal does not turn unseen records into deletion evidence.

## Search and browsing

SQLite WAL holds catalog state and complementary FTS5 `unicode61`/`trigram` indexes. Text normalization is derived; original titles and paths remain unchanged. Search never opens archives or walks roots.

Examples:

```text
artist:someone AND (language:korean OR language:japanese)
path:작가 NOT title:credits
root:ROOT_ID AND substring:archive
word:collection OR tag:series:example
favorite:true
```

Boolean precedence is NOT → AND → OR; adjacency means AND. Quotes preserve phrases and literal operator names. One/two-codepoint terms use indexed token-prefix/exact semantics, not arbitrary substring scans. Longer terms use trigram substring matching by default; `word:`, `prefix:`, `exact:`, `title:`, `path:`, `root:` and exact namespaced tags are available. This is not CJK morphological analysis or transliteration.

Both numbered pagination and virtualized infinite scrolling use the same ACL/offline predicates and stable tie-breakers. Default order is immutable `registered_at DESC, archive_id DESC`; alternatives include natural title and stored filesystem modification time. Cursor traversal can request a refresh after catalog changes. Page sizes are bounded and arbitrary jumps beyond page 10000 are rejected in favor of cursor navigation. Saved searches and favorites belong to the account.

## Reader and cache policy

The reader supports single pages, optional portrait-facing spreads, continuous virtualization, keyboard/pointer/touch navigation, fit/zoom and explicit resize. Wide pages stay whole. Unknown dimensions do not skip pages. Browser-native image representations retain original encoded bytes and animation. AVIF/WebP/JXL capability probes request an explicit PNG compatibility variant only when needed; unavailable decoders return a page-scoped error. A still-frame compatibility result is labeled rather than presented as animation-equivalent.

Immersive creates exactly one router history entry. First Back exits Immersive at the same page; second Back returns to the originating library filters/page/scroll. Page navigation replaces URL state. Browser fullscreen is separate and optional. No persistent reading-history feature is added.

All ZIP/RAR/7z/gallery dependencies are behind [`archive.Adapter`](internal/archive/archive.go): `List`, persisted-entry `Open`, `Close`, explicit cost/solid/session capabilities and optional sequential read-ahead. HTTP code does not call decoder dependencies directly. ZIP/RAR/7z sessions live in a small bounded pool of isolated helpers; gallery access uses the same read-only interface. Session reuse does not mean fresh decoder opens can avoid all format headers, especially solid RAR.

Defaults (see `archika.example.yaml`):

| Budget | Default |
|---|---:|
| Encoded full-resolution RAM cache | 64 MiB |
| Ephemeral decoded-entry disk cache | 1 GiB |
| Maximum encoded page bytes | 64 MiB |
| Archive/decompression concurrency | 2 |
| Active archive sessions | workers + 2 |
| Simultaneous image transforms | 1 |
| Image transformation pixel limit | 64 million |
| Speculative page count | adaptive, at most 12 |
| Speculative bytes per navigation | at most 16 MiB |
| Thumbnail bounding box | 320 × 480 |
| Archive entry count | 100000 |
| Helper wall limit per operation | 30 seconds |

RAM/disk admission and eviction are byte-budgeted; large items do not evict an entire RAM hot set. Cheap ZIP pages do not automatically populate the disk tier. Concurrent work coalesces by source version/entry/representation. Navigation promotes viewing work, predicts forward pages from speed and measured access cost, incorporates cache pressure, and cancels obsolete speculation. Original delivery is not image decoding; archive decompression and optional image transformation have separate budgets.

Persistent thumbnails are lazy, versioned and separate from ephemeral full-resolution bytes. Cover changes affect only the selected work's thumbnail. Unsupported transformations record a stable failure until source change or explicit retry. Optional warm-up is an explicit administrator job. Temporary generations are reclaimed asynchronously under a bounded deletion cadence; startup does not validate a full temporary cache tree or thumbnail tree. Do not share a cache directory between services.

Linux archive/image helpers have 768 MiB data-space, 2 GiB virtual-address-space and 120 CPU-second process ceilings, plus request cancellation/timeouts. These ceilings are not a promise that total container RSS equals the RAM-cache setting: active responses, SQLite, decoder dictionaries, native bitmaps and helpers also consume memory. macOS development has process/time isolation but does not provide the Linux hard data-space cap. Originals remain immutable in every case.

## Jobs and restart

Durable jobs expose progress, failures, pause/resume/retry/cancel and audit records. Lightweight accepted incremental/stability work and bounded automatic crawls can resume after a crash. Full discovery/reconciliation, initial bulk page indexing, metadata backfill, parser reparse and thumbnail warm-up pause after restart. Resume explicitly; restart never invents a new full scan, crawl or thumbnail job.

Global pause stops background execution without blocking HTTP. Lifting global pause does not authorize previously paused-restart bulk work. For an interrupted initial import, resume discovery and explicitly resume/index the remaining page-list scope. On-demand reading never resumes an entire import. Checkpoints and item failures support explicit retry; previous failure counters remain visible. Original files are never repaired, repacked, converted or removed.

## Metadata providers and secrets

Built-ins: E-Hentai/ExHentai, nHentai API v2, Chaika.moe and local JSON. Matching behavior is based on the maintained LANraragi/Ougi sources; they are references, not a Perl runtime or migration layer. Title-only similarity produces review candidates. Confirmed source links and verified provider-specific IDs may link automatically. Provider refresh does not replace filename display titles or invalidate page indexes.

Effective fields honor explicit manual values/clearing, then configurable provider priority, then filename defaults. Provider snapshots, source membership and manual tag additions/exclusions are retained transactionally with FTS updates. Failed fetches preserve successful metadata. Raw payloads are redacted and bounded to 1 MiB, retaining up to three historical results per work/provider.

The default crawler resolver is Cloudflare's DNS-JSON DoH endpoint. Configurable Cloudflare/Google-compatible HTTPS DNS-JSON endpoints and explicit `dns_mode: system` are supported. `proxy_url` accepts an HTTP(S) CONNECT proxy. Destination resolution still follows the selected DNS policy; CONNECT is pinned to checked public IPs while TLS SNI/hostname validation uses the original hostname. Redirects are checked, cross-origin credentials stripped, bodies capped and requests/retries throttled. Proxy endpoint bootstrapping uses the system resolver. DoH does not solve IP/SNI/site blocks, and no silent system-DNS fallback is used.

To persist provider credentials, generate a separate encryption key and supply it outside SQLite:

```sh
bin/archika keygen
# Supply the generated value through ARCHIKA_MASTER_KEY / your secret manager.
```

Admin UI accepts provider-specific credential JSON, for example `{"cookie":"..."}` or `{"token":"..."}`. Credentials are AES-GCM encrypted; values are never returned by management APIs. Do not put credentials in provider configuration, filenames, tags or local JSON. Back up the master key separately from the database; losing it requires re-entering credentials. Rotation does not launch library crawls or invalidate unrelated providers.

Local JSON accepts the normalized result shape:

```json
{"fields":{"title":"Searchable remote title","description":"..."},"tags":["artist:example"],"source_id":"local-record"}
```

Import one work in Metadata, or configure the local provider to read `archive.zip.json` / `gallery/metadata.json` under the authorized root. It performs no network access. Automatic crawling is off until explicitly enabled per root and only new work insertions enqueue it; enabling a policy is not a backfill.

## External plugin protocol v1

Installation registers an administrator-provisioned executable under `plugin_directory`, a manifest and explicit trust confirmation. No remote installer or Go shared-library plugin loader is used. The process starts per invocation and exits afterward. **A subprocess is not a filesystem/network sandbox. Third-party plugins are trusted code.** Use separate OS/container restrictions for untrusted extensions; arbitrary direct sockets bypass the broker policy.

Manifest fields: `id`, `name`, `version`, `protocol_version: 1`, `capabilities: ["metadata"]`, exact `hosts`, requested `permissions`, and `config_schema`. The supported configuration-schema subset is object properties of string/boolean/number/integer, required fields, enums, numeric bounds and string length. Unsupported schema keywords are rejected, not silently treated as full JSON Schema. Other capability families can be declared but v1 host execution negotiates metadata only.

Transport is newline-delimited JSON-RPC 2.0 on stdio, maximum 1 MiB per frame. Host sends `initialize` (ID 1) with protocol/capabilities, then `provider.lookup` (ID 2) with `input`, validated `config` and only that provider's authorized `credentials`. Results are an array of normalized provider results (`source_id`, `source_url`, `fields`, `tags`, `confidence`, `evidence`, `version`, optional `raw`). IDs and negotiated capabilities are checked.

A plugin may issue `host.http` requests while lookup is pending: `method` GET/POST, `url`, `body_base64`, and headers. Successful responses contain `body_base64`; policy/transport/budget failures are JSON-RPC errors. Broker responses must fit the frame ceiling. Cancellation sends `$/cancelRequest` best-effort and kills/reaps the process group. Linux plugins have data-space and CPU ceilings without the archive helper's virtual-address reservation restriction, so runtimes with large sparse reservations are not mistaken for large resident allocations. The deployment's memory/PID/tmpfs limits remain important.

## Verification and limits

```sh
make test
make race
ARCHIKA_SCALE=100000 go test ./internal/app -run TestSearchScale -v -count=1
ARCHIKA_NETWORK=1 go test ./internal/app -run TestDoHAndPinnedProxySmoke -v -count=1
go test ./internal/archive -run '^$' -bench Navigation -benchmem
docker build --target verification -t archika:verify .
```

The verification image contains diagnostic/fixture-generation tools; the runtime image does not. Genuine generated RAR5 image fixtures exercise Unicode paths and the complete 1→2→3→15→14 sequence. Historical RAR4 and malformed/encrypted/solid upstream fixtures retain their attribution. Read-call metrics count source `ReaderAt` calls/bytes, not physical block-device I/O or cold-disk guarantees.

Original ARChika source code is licensed under [Apache-2.0](LICENSE); see [NOTICE](NOTICE). Third-party components retain their respective licenses and attribution in `THIRD_PARTY_NOTICES.txt` and native package copyright files. Fixture measurements do not establish multi-terabyte production RSS or native mobile-browser behavior.
