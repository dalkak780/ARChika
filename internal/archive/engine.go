package archive

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path"
	"sort"
	"strings"
	"syscall"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// Error exposes stable application-facing failure codes without discarding decoder errors.
type Error struct {
	Kind    string
	Err     error
	message string
}

func (e *Error) Error() string { return e.Kind + ": " + e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }
func failure(code, message string) error {
	return &Error{Kind: code, Err: errors.New(message), message: message}
}

// ErrLimit is a resource-policy rejection, not evidence of archive corruption.
var ErrLimit = errors.New("archive resource limit exceeded")

type limitError struct{ message string }

func (e *limitError) Error() string { return ErrLimit.Error() + ": " + e.message }
func (e *limitError) Unwrap() error { return ErrLimit }
func limited(message string) error  { return &limitError{message} }

// Message returns only diagnostics authored here, never decoder or filesystem text.
func Message(err error) string {
	var failure *Error
	if errors.As(err, &failure) {
		return failure.message
	}
	var limit *limitError
	if errors.As(err, &limit) {
		return limit.message
	}
	return ""
}

// Code intentionally leaves unrecognized decoder failures unknown, rather than claiming corruption.
func Code(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	var seven *sevenzip.ReadError
	if errors.As(err, &seven) && seven.Encrypted {
		return "encrypted_or_password_required"
	}
	if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) {
		return "encrypted_or_password_required"
	}
	if errors.Is(err, ErrLimit) {
		return "unknown"
	}
	if errors.Is(err, fs.ErrPermission) {
		return "permission_denied"
	}
	if errors.Is(err, zip.ErrAlgorithm) || errors.Is(err, rardecode.ErrUnknownVersion) || errors.Is(err, rardecode.ErrMultiVolume) {
		return "unsupported_format"
	}
	for _, bad := range []error{zip.ErrFormat, zip.ErrChecksum, zip.ErrInsecurePath, io.ErrUnexpectedEOF, rardecode.ErrShortFile, rardecode.ErrInvalidFileBlock, rardecode.ErrUnexpectedArcEnd, rardecode.ErrBadFileChecksum, rardecode.ErrCorruptBlockHeader, rardecode.ErrCorruptFileHeader, rardecode.ErrBadHeaderCRC, rardecode.ErrDecoderOutOfData} {
		if errors.Is(err, bad) {
			return "corrupt"
		}
	}
	// sevenzip does not export its format/checksum sentinels; match only these exact library errors.
	for cause := err; cause != nil; cause = errors.Unwrap(cause) {
		switch cause.Error() {
		case "sevenzip: not a valid 7-zip file", "sevenzip: checksum error", "sevenzip: incomplete read", "sevenzip: too much data":
			return "corrupt"
		}
	}
	var pe *fs.PathError
	if errors.As(err, &pe) || errors.Is(err, syscall.EIO) {
		return "io_error"
	}
	return "unknown"
}

const defaultEntryBytes = int64(128 << 20)
const defaultEntries = 100000
const skipBudget = int64(512 << 20)

// engine has no source pathname reopen path: all decoders read the retained root-opened handle.
// The host serializes calls, including consuming/closing returned streams.
type engine struct {
	file              *os.File
	gallery           *os.Root
	galleryPath       string
	kind              string
	limits            Limits
	caps              Capabilities
	ctx               context.Context
	zip               *zip.Reader
	zipNames          []string
	salvaged          []zipLocal
	zipOpenError      error
	seven             *sevenzip.Reader
	rar               *rardecode.Reader
	rarFiles          []*rardecode.File
	rarIndex          int
	rarCurrent        *rardecode.FileHeader
	rarRemaining      io.ReadCloser
	active            io.ReadCloser
	closed            bool
	reads, bytes      uint64 // physical read calls/bytes; used by consumer benchmarks
	metadataRemaining int64
	sevenOffsets      []int64
	remainingBudget   int64
	readAhead         bool
	sevenPositions    map[int]int64
}

type sequentialEngine struct{ *engine }

// New opens only this selected container. The caller retains ownership of root.
func New(root *os.Root, name, kind string, limits Limits) (Adapter, error) {
	if limits.MaxEntryBytes <= 0 {
		limits.MaxEntryBytes = defaultEntryBytes
	}
	if limits.MaxEntries <= 0 {
		limits.MaxEntries = defaultEntries
	}
	if name != "." {
		if _, err := normalize(name); err != nil {
			return nil, err
		}
	}
	e := &engine{kind: strings.ToLower(kind), limits: limits, ctx: context.Background(), rarIndex: -1, metadataRemaining: -1}
	e.metadataRemaining = 64 << 20
	switch e.kind {
	case "directory", "dir", "gallery":
		e.kind = "directory"
		gallery, err := root.OpenRoot(".")
		if err != nil {
			return nil, err
		}
		e.gallery = gallery
		e.galleryPath = name
		stat, err := gallery.Stat(name)
		if err != nil || !stat.IsDir() {
			gallery.Close()
			if err != nil {
				return nil, err
			}
			return nil, failure("unsupported_format", "gallery is not a directory")
		}
		e.caps = Capabilities{RandomAccessCost: "constant", Solid: "no", SessionReuse: true, Engine: "os.Root"}
		return e, nil
	case "zip", "cbz", "rar", "cbr", "7z", "cb7":
	default:
		return nil, failure("unsupported_format", "unsupported container kind")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	e.file = f
	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !stat.Mode().IsRegular() {
		f.Close()
		return nil, failure("unsupported_format", "container is not a regular file")
	}
	switch e.kind {
	case "zip", "cbz":
		e.kind = "zip"
		e.caps = Capabilities{RandomAccessCost: "constant", Solid: "no", SessionReuse: true, Engine: "archive/zip"}
		err = e.openZIP(stat.Size())
	case "7z", "cb7":
		e.kind = "7z"
		e.caps = Capabilities{RandomAccessCost: "unknown", Solid: "unknown", SessionReuse: true, Engine: "bodgit/sevenzip"}
		e.seven, err = sevenzip.NewReader(e, stat.Size())
		if err == nil {
			err = e.inspectSeven()
		}
	case "rar", "cbr":
		e.kind = "rar"
		e.caps = Capabilities{RandomAccessCost: "sequential", Solid: "unknown", SessionReuse: true, Engine: "nwaples/rardecode/v2"}
		err = e.resetRAR()
	}
	e.metadataRemaining = -1
	if err != nil {
		f.Close()
		return nil, err
	}
	if e.kind == "rar" || e.kind == "7z" {
		return &sequentialEngine{e}, nil
	}
	return e, nil
}

func (e *engine) budget() int64 { return max(skipBudget, e.limits.MaxEntryBytes) }
func (e *engine) ReadAt(p []byte, off int64) (int, error) {
	if err := e.ctx.Err(); err != nil {
		return 0, err
	}
	if e.metadataRemaining >= 0 {
		if int64(len(p)) > e.metadataRemaining {
			return 0, limited("archive metadata read budget")
		}
		e.metadataRemaining -= int64(len(p))
	}
	n, err := e.file.ReadAt(p, off)
	e.reads++
	e.bytes += uint64(n)
	return n, err
}

func (e *engine) inspectSeven() error {
	if len(e.seven.File) > e.limits.MaxEntries {
		return limited("archive entry count")
	}
	e.sevenOffsets = make([]int64, len(e.seven.File))
	e.sevenPositions = map[int]int64{}
	sizes := map[int]int64{}
	counts := map[int]int{}
	e.caps.Solid, e.caps.RandomAccessCost = "no", "constant"
	for i, f := range e.seven.File {
		if f.FileInfo().IsDir() || f.UncompressedSize == 0 {
			continue
		}
		e.sevenOffsets[i] = sizes[f.Stream]
		if f.UncompressedSize > uint64(math.MaxInt64-sizes[f.Stream]) {
			sizes[f.Stream] = math.MaxInt64
		} else {
			sizes[f.Stream] += int64(f.UncompressedSize)
		}
		counts[f.Stream]++
		if counts[f.Stream] > 1 {
			e.caps.Solid, e.caps.RandomAccessCost = "yes", "sequential"
		}
	}
	return nil
}

func (e *engine) start(ctx context.Context) error {
	if e.closed {
		return fs.ErrClosed
	}
	if e.active != nil {
		return failure("unknown", "previous entry stream must be closed")
	}
	e.ctx = ctx
	if !e.readAhead {
		e.remainingBudget = e.budget()
	}
	return ctx.Err()
}

func (e *engine) List(ctx context.Context) ([]Entry, Capabilities, error) {
	if err := e.start(ctx); err != nil {
		return nil, e.caps, err
	}
	entries := []Entry{}
	seen := map[string]bool{}
	e.caps.Skipped = nil
	skip := func(reason string) {
		if e.caps.Skipped == nil {
			e.caps.Skipped = map[string]int{}
		}
		e.caps.Skipped[reason]++
	}
	count := 0
	found := map[string]int{}
	add := func(name string, size int64, dir, regular, encrypted, unknown bool, locator int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		count++
		if count > e.limits.MaxEntries {
			return limited("archive entry count")
		}
		if dir {
			return nil
		}
		clean := path.Clean(strings.ReplaceAll(name, "\\", "/"))
		if mediaType(clean) == "" {
			ext := strings.ToLower(path.Ext(clean))
			if ext == "" {
				ext = "(none)"
			}
			found[ext]++
			return nil
		}
		if !regular {
			skip("not_regular")
			return nil
		}
		reason := ""
		normal, err := normalize(name)
		switch {
		case encrypted:
			reason = "encrypted"
		case unknown:
			reason = "unknown_size"
		case size < 0 || size > e.limits.MaxEntryBytes:
			reason = "too_large"
		case err != nil:
			reason = "unsafe_path"
		case seen[normal]:
			reason = "duplicate"
		}
		if reason != "" {
			skip(reason)
			return nil
		}
		seen[normal] = true
		hash := sha256.Sum256([]byte(normal))
		entries = append(entries, Entry{Path: normal, ID: hex.EncodeToString(hash[:16]), Size: size, MIME: mediaType(normal), Locator: locator})
		return nil
	}
	var err error
	switch e.kind {
	case "zip":
		if e.zip != nil {
			for i, f := range e.zip.File {
				if err = add(e.zipNames[i], safeSize(f.UncompressedSize64), f.FileInfo().IsDir(), f.Mode().IsRegular(), f.Flags&1 != 0, false, i); err != nil {
					break
				}
			}
		} else {
			for i, f := range e.salvaged {
				if err = add(e.zipNames[i], f.size, strings.HasSuffix(f.name, "/"), true, f.flags&1 != 0, false, i); err != nil {
					break
				}
			}
		}
	case "7z":
		for i, f := range e.seven.File {
			if err = add(f.Name, safeSize(f.UncompressedSize), f.FileInfo().IsDir(), f.Mode().IsRegular(), false, false, i); err != nil {
				break
			}
		}
	case "rar":
		err = e.listRAR()
		if err == nil {
			e.caps.Solid = "no"
			for i, f := range e.rarFiles {
				if f.Solid {
					e.caps.Solid = "yes"
				}
				if err = add(f.Name, f.UnPackedSize, f.IsDir, f.Mode().IsRegular() && f.LinkType == 0, f.Encrypted || f.HeaderEncrypted, f.UnKnownSize, i); err != nil {
					break
				}
			}
		}
	case "directory":
		// Galleries are leaf folders selected by the host. Do not annex child galleries.
		var f *os.File
		f, err = e.gallery.Open(e.galleryPath)
		if err == nil {
			defer f.Close()
			for err == nil {
				if cancel := ctx.Err(); cancel != nil {
					err = cancel
					break
				}
				var batch []fs.DirEntry
				batch, err = f.ReadDir(128)
				for _, d := range batch {
					if d.IsDir() || mediaType(path.Clean(strings.ReplaceAll(d.Name(), "\\", "/"))) == "" {
						if err = add(d.Name(), 0, d.IsDir(), false, false, false, 0); err != nil {
							break
						}
						continue
					}
					var info fs.FileInfo
					info, err = e.gallery.Stat(path.Join(e.galleryPath, d.Name()))
					if err != nil && d.Type()&fs.ModeSymlink != 0 {
						err = add(d.Name(), 0, false, false, false, false, 0)
						if err != nil {
							break
						}
						continue
					}
					if err != nil {
						break
					}
					if err = add(d.Name(), info.Size(), info.IsDir(), info.Mode().IsRegular(), false, false, 0); err != nil {
						break
					}
				}
				if errors.Is(err, io.EOF) {
					err = nil
					break
				}
			}
		}
	}
	if err != nil {
		return nil, e.caps, err
	}
	if len(entries) == 0 {
		if len(e.caps.Skipped) > 0 {
			total, other := 0, 0
			reasons := []string{}
			for reason, count := range e.caps.Skipped {
				total += count
				if reason != "encrypted" {
					other = max(other, count)
				}
				reasons = append(reasons, fmt.Sprintf("%s: %d", reason, count))
			}
			if e.caps.Skipped["encrypted"] > other {
				return nil, e.caps, failure("encrypted_or_password_required", "all image pages are encrypted")
			}
			sort.Strings(reasons)
			return nil, e.caps, failure("unsupported_format", fmt.Sprintf("all %d image pages were skipped (%s)", total, strings.Join(reasons, ", ")))
		}
		if e.zipOpenError != nil {
			return nil, e.caps, e.zipOpenError
		}
		exts := []string{}
		for ext := range found {
			exts = append(exts, ext)
		}
		sort.Slice(exts, func(i, j int) bool {
			if found[exts[i]] != found[exts[j]] {
				return found[exts[i]] > found[exts[j]]
			}
			return exts[i] < exts[j]
		})
		message := "No supported image pages"
		if len(exts) > 0 {
			parts := []string{}
			for _, ext := range exts[:min(5, len(exts))] {
				parts = append(parts, fmt.Sprintf("%d %s", found[ext], ext))
			}
			message += " (found: " + strings.Join(parts, ", ") + ")"
		}
		return nil, e.caps, failure("unsupported_format", message)
	}
	sort.Slice(entries, func(i, j int) bool { return pageLess(entries[i].Path, entries[j].Path) })
	for i := range entries {
		entries[i].Ordinal = i
	}
	return entries, e.caps, nil
}

func safeSize(size uint64) int64 {
	if size > math.MaxInt64 {
		return -1
	}
	return int64(size)
}

func (e *engine) Open(ctx context.Context, entry Entry) (io.ReadCloser, error) {
	if err := e.start(ctx); err != nil {
		return nil, err
	}
	normal, err := normalize(entry.Path)
	if err != nil {
		return nil, err
	}
	if normal != entry.Path || mediaType(normal) == "" {
		return nil, failure("corrupt", "invalid persisted page path")
	}
	if entry.Size < 0 {
		return nil, failure("corrupt", "invalid page size")
	}
	if entry.Size > e.limits.MaxEntryBytes {
		return nil, limited("page bytes")
	}
	if entry.Size > e.remainingBudget {
		return nil, limited("cumulative decompression byte budget")
	}
	e.remainingBudget -= entry.Size
	var reader io.ReadCloser
	var actual string
	var size int64
	switch e.kind {
	case "directory":
		if path.Base(normal) != normal {
			return nil, failure("permission_denied", "page is outside selected gallery")
		}
		var f *os.File
		f, err = e.gallery.Open(path.Join(e.galleryPath, normal))
		if err == nil {
			var stat fs.FileInfo
			stat, err = f.Stat()
			if err != nil {
				f.Close()
				break
			}
			if !stat.Mode().IsRegular() {
				f.Close()
				return nil, failure("unsupported_format", "page is not a regular file")
			}
			reader, actual, size = f, normal, stat.Size()
		}
	case "zip":
		if entry.Locator < 0 || entry.Locator >= len(e.zipNames) {
			return nil, failure("corrupt", "stale page locator")
		}
		actual = e.zipNames[entry.Locator]
		if e.zip == nil {
			f := e.salvaged[entry.Locator]
			size = f.size
			if _, err = checkEntry(entry, actual, size, e.limits); err != nil {
				return nil, err
			}
			if f.flags&1 != 0 {
				return nil, failure("encrypted_or_password_required", "encrypted ZIP entry")
			}
			reader = e.openLocalZIP(f)
		} else {
			f := e.zip.File[entry.Locator]
			size = safeSize(f.UncompressedSize64)
			if f.Flags&1 != 0 {
				return nil, failure("encrypted_or_password_required", "encrypted ZIP entry")
			}
			if !f.Mode().IsRegular() {
				return nil, failure("unsupported_format", "page is not a regular file")
			}
			if _, err = checkEntry(entry, actual, size, e.limits); err != nil {
				return nil, err
			}
			reader, err = f.Open()
		}
	case "7z":
		if entry.Locator < 0 || entry.Locator >= len(e.seven.File) {
			return nil, failure("corrupt", "stale page locator")
		}
		f := e.seven.File[entry.Locator]
		actual, size = f.Name, safeSize(f.UncompressedSize)
		if _, err = checkEntry(entry, actual, size, e.limits); err != nil {
			return nil, err
		}
		if !f.Mode().IsRegular() {
			return nil, failure("unsupported_format", "page is not a regular file")
		}
		// Account for the dependency's internal solid seek before it can decode skipped bytes.
		position := e.sevenPositions[f.Stream]
		skip := e.sevenOffsets[entry.Locator]
		if position <= skip {
			skip -= position
		}
		if skip > e.remainingBudget {
			return nil, limited("solid stream decompression byte budget")
		}
		e.remainingBudget -= skip
		reader, err = f.Open()
	case "rar":
		reader, actual, size, err = e.openRAR(ctx, entry)
	}
	if err != nil {
		return nil, err
	}
	if _, err = checkEntry(entry, actual, size, e.limits); err != nil {
		reader.Close()
		return nil, err
	}
	r := &pageReader{ctx: ctx, source: reader, remaining: size, release: func() { e.active = nil }}
	if e.kind == "7z" {
		stream, offset := e.seven.File[entry.Locator].Stream, e.sevenOffsets[entry.Locator]
		r.release = func() { e.sevenPositions[stream] = offset + size - r.remaining; e.active = nil }
	}
	e.active = r
	return r, nil
}

func checkEntry(entry Entry, actual string, size int64, limits Limits) (string, error) {
	normal, err := normalize(actual)
	if err != nil {
		return "", err
	}
	if normal != entry.Path || size != entry.Size {
		return "", failure("corrupt", "persisted entry no longer matches source")
	}
	if size < 0 {
		return "", failure("corrupt", "invalid uncompressed page size")
	}
	if size > limits.MaxEntryBytes {
		return "", limited("page bytes")
	}
	return normal, nil
}

// pageReader checks every output read, bounds uncompressed bytes, and does not let io.Copy bypass cancellation.
type pageReader struct {
	ctx       context.Context
	source    io.ReadCloser
	remaining int64
	release   func()
	done      bool
	verified  bool
}

func (r *pageReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, fs.ErrClosed
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.remaining == 0 {
		if r.verified {
			return 0, io.EOF
		}
		var probe [1]byte
		n, err := r.source.Read(probe[:])
		if n > 0 {
			return 0, failure("corrupt", "entry exceeds declared size")
		}
		if err == io.EOF {
			r.verified = true
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.source.Read(p)
	r.remaining -= int64(n)
	if err == io.EOF && r.remaining > 0 {
		return n, io.ErrUnexpectedEOF
	}
	return n, err
}
func (r *pageReader) Close() error {
	if r.done {
		return nil
	}
	r.done = true
	err := r.source.Close()
	r.release()
	return err
}

func (e *engine) Close() error {
	if e.closed {
		return nil
	}
	e.closed = true
	var err error
	if e.active != nil {
		err = e.active.Close()
	}
	// sevenzip.Reader owns no OS handles; dropping it releases its bounded stream reuse pool.
	e.seven, e.zip, e.rar, e.rarFiles = nil, nil, nil, nil
	if e.file != nil {
		err = errors.Join(err, e.file.Close())
	}
	if e.gallery != nil {
		err = errors.Join(err, e.gallery.Close())
	}
	return err
}

func (s *sequentialEngine) ReadAhead(ctx context.Context, entries []Entry, consume func(Entry, io.Reader) error) error {
	if err := s.start(ctx); err != nil {
		return err
	}
	s.readAhead = true
	defer func() { s.readAhead = false }()
	// Physical locator order avoids repeated solid-stream resets; caller identifies pages by Entry.
	ordered := append([]Entry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Locator < ordered[j].Locator })
	var total int64
	for _, entry := range ordered {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Size < 0 || entry.Size > s.budget()-total {
			return limited("read-ahead bytes")
		}
		total += entry.Size
		r, err := s.Open(ctx, entry)
		if err != nil {
			return err
		}
		err = consume(entry, r)
		if err == nil {
			_, err = io.Copy(io.Discard, r)
		}
		err = errors.Join(err, r.Close())
		if err != nil {
			return err
		}
	}
	return nil
}

func normalize(name string) (string, error) {
	if len(name) > 4096 {
		return "", limited("entry path length")
	}
	name = strings.TrimLeft(strings.ReplaceAll(name, "\\", "/"), "/")
	driveAbsolute := len(name) >= 3 && (name[0] >= 'A' && name[0] <= 'Z' || name[0] >= 'a' && name[0] <= 'z') && name[1:3] == ":/"
	if name == "" || driveAbsolute || strings.IndexByte(name, 0) >= 0 {
		return "", failure("corrupt", "unsafe entry path")
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", failure("corrupt", "entry path traversal")
		}
	}
	normal := path.Clean(name)
	if normal == "." || !fs.ValidPath(normal) {
		return "", failure("corrupt", "unsafe entry path")
	}
	return normal, nil
}
func mediaType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".jfif", ".jpe":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".apng":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".avif":
		return "image/avif"
	case ".jxl":
		return "image/jxl"
	}
	return ""
}
func pageClass(name string) int {
	base := strings.ToLower(strings.TrimSuffix(path.Base(name), path.Ext(name)))
	words := strings.FieldsFunc(base, func(r rune) bool {
		return r == ' ' || r == '_' || r == '-' || r == '.' || r == '(' || r == ')' || r == '[' || r == ']'
	})
	for _, word := range words {
		switch word {
		case "cover", "front":
			return -1
		case "credit", "credits", "note", "notes":
			return 1
		}
	}
	return 0
}
func pageLess(a, b string) bool {
	if ca, cb := pageClass(a), pageClass(b); ca != cb {
		return ca < cb
	}
	la, lb := strings.ToLower(a), strings.ToLower(b)
	for i, j := 0, 0; i < len(la) && j < len(lb); {
		if la[i] >= '0' && la[i] <= '9' && lb[j] >= '0' && lb[j] <= '9' {
			ie, je := i, j
			for ie < len(la) && la[ie] >= '0' && la[ie] <= '9' {
				ie++
			}
			for je < len(lb) && lb[je] >= '0' && lb[je] <= '9' {
				je++
			}
			an, bn := strings.TrimLeft(la[i:ie], "0"), strings.TrimLeft(lb[j:je], "0")
			if len(an) != len(bn) {
				return len(an) < len(bn)
			}
			if an != bn {
				return an < bn
			}
			i, j = ie, je
		} else {
			if la[i] != lb[j] {
				return la[i] < lb[j]
			}
			i++
			j++
		}
		if i == len(la) && j != len(lb) {
			return true
		}
		if j == len(lb) && i != len(la) {
			return false
		}
	}
	if la != lb {
		return la < lb
	}
	return a < b
}

// rarFS prevents the dependency's volume manager from reopening source paths or following volumes.
// Each cursor shares the already root-contained, read-only source handle.
type rarFS struct{ e *engine }

func (r rarFS) Open(name string) (fs.File, error) {
	if name != "source.rar" {
		return nil, failure("unsupported_format", "multi-volume RAR is unsupported")
	}
	stat, err := r.e.file.Stat()
	if err != nil {
		return nil, err
	}
	return &rarCursor{SectionReader: io.NewSectionReader(r.e, 0, stat.Size()), info: stat}, nil
}

type rarCursor struct {
	*io.SectionReader
	info fs.FileInfo
}

func (r *rarCursor) Close() error               { return nil }
func (r *rarCursor) Stat() (fs.FileInfo, error) { return r.info, nil }

func (e *engine) resetRAR() error {
	stat, err := e.file.Stat()
	if err != nil {
		return err
	}
	e.rar, err = rardecode.NewReader(io.NewSectionReader(e, 0, stat.Size()), rardecode.MaxDictionarySize(max(256<<10, e.limits.MaxEntryBytes)))
	e.rarIndex, e.rarCurrent, e.rarRemaining = -1, nil, nil
	return err
}

func (e *engine) listRAR() error {
	e.metadataRemaining = min(min(int64(e.limits.MaxEntries), 16384)*4096+65536, int64(64<<20))
	defer func() { e.metadataRemaining = -1 }()
	var err error
	e.rarFiles, err = rardecode.List("source.rar", rardecode.FileSystem(rarFS{e}), rardecode.MaxDictionarySize(max(256<<10, e.limits.MaxEntryBytes)))
	if err == nil && len(e.rarFiles) > e.limits.MaxEntries {
		return limited("archive entry count")
	}
	return err
}

func (e *engine) openRAR(ctx context.Context, entry Entry) (io.ReadCloser, string, int64, error) {
	if entry.Locator < 0 || entry.Locator >= e.limits.MaxEntries {
		return nil, "", 0, failure("corrupt", "stale RAR locator")
	}
	if e.rarFiles == nil {
		if err := e.listRAR(); err != nil {
			return nil, "", 0, err
		}
	}
	if entry.Locator >= len(e.rarFiles) {
		return nil, "", 0, failure("corrupt", "stale RAR locator")
	}
	f := e.rarFiles[entry.Locator]
	if !f.Solid {
		if _, err := checkEntry(entry, f.Name, f.UnPackedSize, e.limits); err != nil {
			return nil, "", 0, err
		}
		r, err := f.Open()
		return r, f.Name, f.UnPackedSize, err
	}
	if e.rar == nil || entry.Locator <= e.rarIndex {
		if err := e.resetRAR(); err != nil {
			return nil, "", 0, err
		}
	}
	for e.rarIndex < entry.Locator {
		if err := ctx.Err(); err != nil {
			return nil, "", 0, err
		}
		// Drain explicitly before Next: Next's implicit solid drain cannot enforce our output budget.
		if e.rarRemaining != nil {
			reader := &contextReader{ctx: ctx, r: e.rarRemaining}
			n, err := io.Copy(io.Discard, io.LimitReader(reader, e.remainingBudget+1))
			e.remainingBudget -= n
			if err != nil {
				e.resetRAR()
				return nil, "", 0, err
			}
			if e.remainingBudget < 0 {
				e.resetRAR()
				return nil, "", 0, limited("RAR cumulative decompression byte budget")
			}
		}
		h, err := e.rar.Next()
		if err != nil {
			return nil, "", 0, err
		}
		e.rarIndex++
		e.rarCurrent = h
		if h.Encrypted || h.HeaderEncrypted {
			if e.rarIndex == entry.Locator {
				return nil, "", 0, failure("encrypted_or_password_required", "encrypted RAR entry")
			}
			e.rarRemaining = nil
			continue
		}
		if h.UnKnownSize || h.UnPackedSize < 0 {
			if e.rarIndex == entry.Locator {
				return nil, "", 0, failure("corrupt", "invalid RAR entry size")
			}
			e.rarRemaining = io.NopCloser(e.rar)
		} else {
			e.rarRemaining = &rarRemainder{r: e.rar, remaining: h.UnPackedSize}
		}
	}
	h := e.rarCurrent
	if h == nil || !h.Mode().IsRegular() || h.LinkType != 0 {
		return nil, "", 0, failure("unsupported_format", "RAR page is not a regular file")
	}
	return e.rarRemaining, h.Name, h.UnPackedSize, nil
}

// Closing a page leaves the forward decoder in place; subsequent navigation drains its bounded remainder.
type rarRemainder struct {
	r         io.Reader
	remaining int64
}

func (r *rarRemainder) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return r.r.Read(p[:min(len(p), 1)])
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.r.Read(p)
	r.remaining -= int64(n)
	return n, err
}
func (r *rarRemainder) Close() error { return nil }

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

var _ Adapter = (*engine)(nil)
var _ Sequential = (*sequentialEngine)(nil)
