// Package archive provides read-only, root-contained access to page containers.
// Decoder dependencies must not escape this package.
package archive

import (
	"context"
	"io"
)

type Entry struct {
	Path    string `json:"entry"`
	ID      string `json:"entry_id"`
	Ordinal int    `json:"ordinal"`
	Size    int64  `json:"size"`
	MIME    string `json:"mime"`
	Locator int    `json:"-"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
}
type Capabilities struct {
	RandomAccessCost string `json:"random_access_cost"` // constant, sequential, or unknown
	Solid            string `json:"solid"`              // yes, no, or unknown
	SessionReuse     bool   `json:"session_reuse"`
	Engine           string `json:"engine"`
}
type Limits struct {
	MaxEntryBytes int64
	MaxEntries    int
}

// Adapter owns its decoder/file. Calls on one instance are serialized by the host.
// List is used only when building a persistent manifest; Open accepts persisted entries.
// Context cancellation closes bounded reads; no method writes source files.
type Adapter interface {
	List(context.Context) ([]Entry, Capabilities, error)
	Open(context.Context, Entry) (io.ReadCloser, error)
	Close() error
}

// Sequential is an optional capability for amortizing solid-stream reads.
type Sequential interface {
	ReadAhead(context.Context, []Entry, func(Entry, io.Reader) error) error
}

// New is implemented by the format dispatch in engine.go. The root is caller-owned.
// Implementations must use root-relative handles, not reopen an absolute pathname.
