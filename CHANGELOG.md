# Changelog

## Unreleased

- Prevent reader pages from briefly rendering at a placeholder aspect ratio before expanding to their decoded size, including cached page turns and spread layout.

- Add configurable structured server logging for startup, jobs, scans, indexing, authentication, administration, providers, helpers and HTTP failures.

- License original project source under Apache-2.0 and include project and third-party notices in distributions.

- Implement Go `net/http` server with an embedded React/Vite SPA, SQLite WAL migrations, local Argon2id accounts, server-side sessions, root grants, preferences and favorites.
- Add explicit resumable two-stage discovery, stable-file admission, incremental filesystem watching, physical aliases, offline retention, bounded root relocation verification and administrator-confirmed move handling. No startup library scan or recursive ownership repair.
- Unify ZIP/RAR/7z/gallery reading behind `archive.Adapter`; persist natural page manifests and capabilities, isolate decoders, reuse bounded sessions and retain original encoded image delivery.
- Add bounded RAM/ephemeral disk caching, shared work promotion/cancellation, adaptive prefetch, lazy persistent covers, bounded thumbnails and negotiated compatibility conversion. Production image processing uses existing govips/libvips bindings; pure-Go development remains available.
- Add Boolean Unicode/trigram search, ACL-aware counts and cursor/numbered navigation, bounded infinite/continuous virtualization, facing-page layout, keyboard/pointer/touch input and router-managed two-step Immersive Back.
- Add secure DoH/proxy crawler transport, E-Hentai/ExHentai, nHentai v2, Chaika and local JSON providers, provenance/manual-override merging, candidate approval and versioned trusted subprocess plugins.
- Add durable type-specific restart policy, failed-item retries, directory exclusion locks, read-only container deployment and source/codec/ACL/recovery regression checks. Runtime verification evidence is recorded separately; fixture results are not production-scale or hardware-browser guarantees.
