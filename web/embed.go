package web

import "embed"

// Assets contains the production SPA. Run pnpm --dir web build before compiling Go.
//
//go:embed all:dist
var Assets embed.FS
