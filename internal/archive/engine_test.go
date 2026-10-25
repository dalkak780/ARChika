package archive

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func makeZIP(tb testing.TB, dir string, names []string, payload []byte) string {
	tb.Helper()
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, name := range names {
		w, err := writer.Create(name)
		if err != nil {
			tb.Fatal(err)
		}
		if _, err := w.Write(payload); err != nil {
			tb.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		tb.Fatal(err)
	}
	name := filepath.Join(dir, "pages.zip")
	if err := os.WriteFile(name, data.Bytes(), 0600); err != nil {
		tb.Fatal(err)
	}
	return "pages.zip"
}
func openRoot(tb testing.TB, dir string) *os.Root {
	tb.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { root.Close() })
	return root
}
func openAdapter(tb testing.TB, root *os.Root, name, kind string, limits Limits) Adapter {
	tb.Helper()
	a, err := New(root, name, kind, limits)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { a.Close() })
	return a
}
func manifest(tb testing.TB, a Adapter) ([]Entry, Capabilities) {
	tb.Helper()
	entries, caps, err := a.List(context.Background())
	if err != nil {
		tb.Fatal(err)
	}
	return entries, caps
}
func pageBytes(tb testing.TB, a Adapter, e Entry) []byte {
	tb.Helper()
	r, err := a.Open(context.Background(), e)
	if err != nil {
		tb.Fatal(err)
	}
	data, err := io.ReadAll(r)
	closeErr := r.Close()
	if err != nil {
		tb.Fatal(err)
	}
	if closeErr != nil {
		tb.Fatal(closeErr)
	}
	if int64(len(data)) != e.Size {
		tb.Fatalf("size %d != %d", len(data), e.Size)
	}
	return data
}

func TestZIPManifestAndPersistedOpen(t *testing.T) {
	dir := t.TempDir()
	name := makeZIP(t, dir, []string{"chapter/10.JPG", "chapter/2.png", "chapter/1.webp", "credits.png", "cover.jpg", "notes.avif", "inside.zip", "ignored.txt", "chapter/3.apng", "chapter/4.gif", "chapter/5.jpeg", "chapter/6.jxl"}, []byte("pixels are not decoded by List"))
	root := openRoot(t, dir)
	a := openAdapter(t, root, name, "zip", Limits{})
	entries, caps := manifest(t, a)
	want := []string{"cover.jpg", "chapter/1.webp", "chapter/2.png", "chapter/3.apng", "chapter/4.gif", "chapter/5.jpeg", "chapter/6.jxl", "chapter/10.JPG", "credits.png", "notes.avif"}
	got := []string{}
	ids := map[string]bool{}
	for i, e := range entries {
		got = append(got, e.Path)
		if e.Ordinal != i || e.ID == "" || ids[e.ID] {
			t.Fatalf("unstable manifest entry: %+v", e)
		}
		ids[e.ID] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
	if caps.RandomAccessCost != "constant" || caps.Solid != "no" || !caps.SessionReuse {
		t.Fatalf("capabilities %+v", caps)
	}
	if entries[7].MIME != "image/jpeg" || entries[3].MIME != "image/png" || entries[6].MIME != "image/jxl" {
		t.Fatalf("media types %+v", entries)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openAdapter(t, root, name, "zip", Limits{})
	for _, n := range []int{0, 1, 2, 7, 6} {
		pageBytes(t, reopened, entries[n])
	}
	second, _ := manifest(t, reopened)
	if !reflect.DeepEqual(entries, second) {
		t.Fatal("manifest changed across sessions")
	}
	stale := entries[0]
	stale.Locator = entries[1].Locator
	if r, err := reopened.Open(context.Background(), stale); err == nil {
		r.Close()
		t.Fatal("stale locator accepted")
	}
}

func TestEntryCountLimit(t *testing.T) {
	dir := t.TempDir()
	name := makeZIP(t, dir, []string{"1.png", "ignored.txt"}, []byte("page"))
	a := openAdapter(t, openRoot(t, dir), name, "zip", Limits{MaxEntries: 1})
	_, _, err := a.List(context.Background())
	if !errors.Is(err, ErrLimit) || Code(err) == "corrupt" {
		t.Fatalf("resource error misclassified: %v (%s)", err, Code(err))
	}
}

func TestDirectoryStaysWithinSelectedGallery(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "gallery", "child"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"gallery/2.png": "second", "gallery/10.png": "tenth", "gallery/child/1.png": "another gallery", "outside.png": "outside"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../outside.png", filepath.Join(dir, "gallery", "within-root.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(t.TempDir(), "unavailable.png"), filepath.Join(dir, "gallery", "escape.png")); err != nil {
		t.Fatal(err)
	}
	a := openAdapter(t, openRoot(t, dir), "gallery", "directory", Limits{})
	entries, _ := manifest(t, a)
	if len(entries) != 3 || entries[0].Path != "2.png" || entries[1].Path != "10.png" || entries[2].Path != "within-root.png" {
		t.Fatalf("gallery manifest %+v", entries)
	}
	if string(pageBytes(t, a, entries[2])) != "outside" {
		t.Fatal("in-root symlink did not retain source bytes")
	}
	if string(pageBytes(t, a, entries[0])) != "second" {
		t.Fatal("wrong gallery bytes")
	}
	for _, name := range []string{"../outside.png", "child/1.png", "escape.png"} {
		e := Entry{Path: name, Size: 7}
		if r, err := a.Open(context.Background(), e); err == nil {
			r.Close()
			t.Fatalf("escaped gallery via %q", name)
		}
	}
	if _, err := New(openRoot(t, dir), "../outside.png", "zip", Limits{}); err == nil {
		t.Fatal("root traversal accepted")
	}
}

func TestRAR4PersistedPage(t *testing.T) {
	root := openRoot(t, "testdata")
	a := openAdapter(t, root, "pages-rar4.rar", "rar", Limits{})
	entries, caps := manifest(t, a)
	if len(entries) != 2 || entries[0].Path != "test.png" || entries[1].Path != "testdir/test.png" || caps.Solid != "no" {
		t.Fatalf("RAR4 pages=%v caps=%+v", entries, caps)
	}
	a.Close()
	fresh := openAdapter(t, root, "pages-rar4.rar", "rar", Limits{})
	for _, entry := range entries {
		if string(pageBytes(t, fresh, entry)) != "test text document\r\n" {
			t.Fatal("RAR4 persisted nested page bytes differ from fixture")
		}
	}
}

func TestSolidSeekBudgetAndDecoderLimits(t *testing.T) {
	for _, tc := range []struct{ name, kind string }{{"pages-solid-on.7z", "7z"}, {"pages-rar5-solid.rar", "rar"}} {
		t.Run(tc.kind, func(t *testing.T) {
			root := openRoot(t, "testdata")
			for _, limits := range []Limits{{MaxEntries: 1}} {
				a, err := New(root, tc.name, tc.kind, limits)
				if err == nil {
					_, _, err = a.List(context.Background())
					a.Close()
				}
				if !errors.Is(err, ErrLimit) {
					t.Fatalf("decoder limit not enforced: %v", err)
				}
			}
			a := openAdapter(t, root, tc.name, tc.kind, Limits{})
			entries, _ := manifest(t, a)
			s := a.(*sequentialEngine)
			// Simulate an almost-spent read-ahead budget without a giant bomb fixture.
			s.readAhead = true
			s.remainingBudget = entries[2].Size + 1
			if r, err := a.Open(context.Background(), entries[2]); !errors.Is(err, ErrLimit) {
				if r != nil {
					r.Close()
				}
				t.Fatalf("solid skip escaped cumulative budget: %v", err)
			}
			s.readAhead = false
			pageBytes(t, a, entries[0])
		})
	}
}

func TestRetainedSourceHandleSurvivesPathReplacement(t *testing.T) {
	dir := t.TempDir()
	name := makeZIP(t, dir, []string{"1.png"}, []byte("original page"))
	root := openRoot(t, dir)
	a := openAdapter(t, root, name, "zip", Limits{})
	entries, _ := manifest(t, a)
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(dir, "old.zip")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not the open source"), 0600); err != nil {
		t.Fatal(err)
	}
	if string(pageBytes(t, a, entries[0])) != "original page" {
		t.Fatal("adapter reopened changed pathname")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Stat("old.zip"); err != nil {
		t.Fatalf("adapter closed caller root: %v", err)
	}
}

func TestSevenzipAndRARConsumerSessions(t *testing.T) {
	// RAR fixture extensions were renamed from .bin to .png solely to exercise page routing.
	// Their payloads remain upstream binary test patterns, not claimed to be PNG pixels.
	for _, tc := range []struct {
		name, kind, solid string
		pages             int
	}{
		{"pages-solid-on.7z", "7z", "yes", 16},
		{"pages-solid-off.7z", "7z", "no", 16},
		{"pages-rar5-solid.rar", "rar", "yes", 4},
		{"pages-rar5-nonsolid.rar", "rar", "no", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := openRoot(t, "testdata")
			a := openAdapter(t, root, tc.name, tc.kind, Limits{})
			entries, caps := manifest(t, a)
			if len(entries) != tc.pages || caps.Solid != tc.solid || !caps.SessionReuse {
				t.Fatalf("manifest=%d caps=%+v", len(entries), caps)
			}
			if tc.solid == "yes" && caps.RandomAccessCost != "sequential" {
				t.Fatalf("false random-access claim %+v", caps)
			}
			want := map[int][]byte{}
			jump := min(14, len(entries)-1)
			for _, n := range []int{0, 1, 2, jump, jump - 1} {
				want[n] = pageBytes(t, a, entries[n])
			}
			// Closing a partially read page must leave forward navigation usable.
			r, err := a.Open(context.Background(), entries[0])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := r.Read(make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(pageBytes(t, a, entries[1]), want[1]) {
				t.Fatal("forward decoder lost remainder")
			}
			seq, ok := a.(Sequential)
			if !ok {
				t.Fatal("missing sequential capability")
			}
			count := 0
			if err := seq.ReadAhead(context.Background(), []Entry{entries[2], entries[0], entries[1]}, func(e Entry, r io.Reader) error {
				data, err := io.ReadAll(r)
				if err != nil {
					return err
				}
				if !bytes.Equal(data, want[e.Ordinal]) {
					return errors.New("read-ahead bytes differ")
				}
				count++
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if count != 3 {
				t.Fatalf("read-ahead callbacks %d", count)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if err := seq.ReadAhead(ctx, entries[:2], func(e Entry, r io.Reader) error { cancel(); _, err := r.Read(make([]byte, 1)); return err }); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation ignored: %v", err)
			}
			if err := a.Close(); err != nil {
				t.Fatal(err)
			}
			fresh := openAdapter(t, root, tc.name, tc.kind, Limits{})
			// Deliberately no List: locators came from the previous session's persistent manifest.
			if !bytes.Equal(pageBytes(t, fresh, entries[jump]), want[jump]) {
				t.Fatal("persisted entry changed after new session")
			}
			if !bytes.Equal(pageBytes(t, fresh, entries[0]), want[0]) {
				t.Fatal("backward reset changed bytes")
			}
		})
	}
}

func TestUpstreamSevenzipFixtureAndErrors(t *testing.T) {
	root := openRoot(t, "testdata")
	a := openAdapter(t, root, "upstream-lzma.7z", "7z", Limits{})
	entries, caps, err := a.List(context.Background())
	if Code(err) != "unsupported_format" || len(entries) != 0 || caps.Solid != "yes" {
		t.Fatalf("upstream fixture caps=%+v, pages=%d", caps, len(entries))
	}
	for _, name := range []string{"test_read_format_rar5_encrypted.rar"} {
		a, err := New(root, name, "rar", Limits{})
		if err == nil {
			_, _, err = a.List(context.Background())
			a.Close()
		}
		if Code(err) != "unsupported_format" {
			t.Fatalf("encrypted non-image entries: %v", err)
		}
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.zip"), []byte("not an archive"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err = New(openRoot(t, dir), "bad.zip", "zip", Limits{})
	if Code(err) != "corrupt" {
		t.Fatalf("corruption: %v (%s)", err, Code(err))
	}
	if Code(errors.New("unrecognized decoder error")) != "unknown" {
		t.Fatal("invented corruption classification")
	}
	if Code(os.ErrPermission) != "permission_denied" {
		t.Fatal("permission classification")
	}
	if _, err := New(root, "upstream-lzma.7z", "tar", Limits{}); Code(err) != "unsupported_format" {
		t.Fatalf("unsupported kind: %v", err)
	}
}

// BenchmarkNavigation includes opening the retained decoder and the 1,2,3,15,14 page pattern.
// file_reads/op and file_bytes/op are actual calls/bytes at the read-only source handle,
// not physical disk sectors or cache-miss counts. Standard ns/op and B/op cover time/allocations.
func BenchmarkNavigation(b *testing.B) {
	for _, kind := range []string{"zip", "7z-solid", "7z-nonsolid", "rar5-solid", "rar5-nonsolid"} {
		b.Run(kind, func(b *testing.B) {
			var root *os.Root
			var name, format string
			if kind == "zip" {
				dir := b.TempDir()
				names := []string{}
				for i := 1; i <= 16; i++ {
					names = append(names, fmt.Sprintf("%d.png", i))
				}
				name = makeZIP(b, dir, names, bytes.Repeat([]byte("page"), 1024))
				format = "zip"
				root = openRoot(b, dir)
			} else {
				root = openRoot(b, "testdata")
				switch kind {
				case "7z-solid":
					name, format = "pages-solid-on.7z", "7z"
				case "7z-nonsolid":
					name, format = "pages-solid-off.7z", "7z"
				case "rar5-solid":
					name, format = "real-pages-rar5-solid.rar", "rar"
				case "rar5-nonsolid":
					name, format = "real-pages-rar5-nonsolid.rar", "rar"
				}
			}
			seed := openAdapter(b, root, name, format, Limits{})
			entries, _ := manifest(b, seed)
			seed.Close()
			var reads, readBytes uint64
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				a, err := New(root, name, format, Limits{})
				if err != nil {
					b.Fatal(err)
				}
				for _, page := range []int{0, 1, 2, 14, 13} {
					r, err := a.Open(context.Background(), entries[page])
					if err != nil {
						b.Fatal(err)
					}
					if _, err = io.Copy(io.Discard, r); err != nil {
						b.Fatal(err)
					}
					if err = r.Close(); err != nil {
						b.Fatal(err)
					}
				}
				var e *engine
				if s, ok := a.(*sequentialEngine); ok {
					e = s.engine
				} else {
					e = a.(*engine)
				}
				reads += e.reads
				readBytes += e.bytes
				if err = a.Close(); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			b.ReportMetric(float64(reads)/float64(b.N), "file_reads/op")
			b.ReportMetric(float64(readBytes)/float64(b.N), "file_bytes/op")
		})
	}
}

func TestDirectoryWithoutImagePages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "private.BMP"), []byte("bitmap"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "child.png"), 0700); err != nil {
		t.Fatal(err)
	}
	a := openAdapter(t, openRoot(t, dir), ".", "directory", Limits{})
	_, _, err := a.List(context.Background())
	if Code(err) != "unsupported_format" || Message(err) != "No supported image pages (found: 1 .bmp)" {
		t.Fatalf("empty gallery: %v", err)
	}
}
