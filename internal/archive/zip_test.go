package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
)

func saveZIP(t *testing.T, data []byte) *os.Root {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pages.zip"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return openRoot(t, dir)
}

func TestZIPLegacyNames(t *testing.T) {
	for _, tc := range []struct {
		name     string
		encoding encoding.Encoding
		names    []string
	}{
		{"CP949", korean.EUCKR, []string{"가01.png", "나02.png", "다03.png"}},
		{"ShiftJIS", japanese.ShiftJIS, []string{"あ01.png", "い02.png", "う03.png"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			writer := zip.NewWriter(&data)
			for _, name := range tc.names {
				raw, err := tc.encoding.NewEncoder().String(name)
				if err != nil {
					t.Fatal(err)
				}
				w, err := writer.CreateHeader(&zip.FileHeader{Name: raw, NonUTF8: true, Method: zip.Deflate})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.Write([]byte(name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			root := saveZIP(t, data.Bytes())
			a := openAdapter(t, root, "pages.zip", "zip", Limits{})
			entries, _ := manifest(t, a)
			if len(entries) != 3 {
				t.Fatalf("entries: %+v", entries)
			}
			a.Close()
			fresh := openAdapter(t, root, "pages.zip", "zip", Limits{})
			for i, entry := range entries {
				if entry.Path != tc.names[i] || string(pageBytes(t, fresh, entry)) != tc.names[i] {
					t.Fatalf("entry: %+v", entry)
				}
			}
		})
	}
}

func TestZIPNameFallbackAndUTF8(t *testing.T) {
	names := []zipName{{"가01.png", 0}, {"あ01.png", 0x800}, {"\xff01.png", 0}, {"\xff01.png", 0}}
	want := []string{"가01.png", "あ01.png", "�01.png", "�01.png"}
	if got := decodeZIPNames(names); !reflect.DeepEqual(got, want) {
		t.Fatalf("%q", got)
	}
}

func TestZIPSkipsBadEntries(t *testing.T) {
	var data bytes.Buffer
	writer := zip.NewWriter(&data)
	for _, header := range []zip.FileHeader{
		{Name: "../readme.txt", Flags: 1, UncompressedSize64: 1000},
		{Name: "../folder/"},
		{Name: "Re:Zero/001.png"},
		{Name: "002.png"},
		{Name: "002.png"},
		{Name: "private.png", Flags: 1},
		{Name: "../evil.png"},
		{Name: "/absolute.png"},
		{Name: "C:\\absolute.png"},
		{Name: "nul\x00.png"},
		{Name: "large.png", UncompressedSize64: 1000},
		{Name: "overflow.png", UncompressedSize64: 1 << 63},
	} {
		header.CompressedSize64 = 4
		header.CRC32 = crc32.ChecksumIEEE([]byte("page"))
		if header.UncompressedSize64 == 0 {
			header.UncompressedSize64 = 4
		}
		w, err := writer.CreateRaw(&header)
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(header.Name, "/") {
			continue
		}
		if _, err = w.Write([]byte("page")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	root := saveZIP(t, data.Bytes())
	a := openAdapter(t, root, "pages.zip", "zip", Limits{MaxEntryBytes: 8})
	entries, caps := manifest(t, a)
	want := map[string]int{"encrypted": 1, "unsafe_path": 3, "duplicate": 1, "too_large": 2}
	if len(entries) != 3 || !reflect.DeepEqual(caps.Skipped, want) {
		t.Fatalf("entries=%+v caps=%+v", entries, caps)
	}
	_, again := manifest(t, a)
	if !reflect.DeepEqual(again.Skipped, want) {
		t.Fatalf("repeated List: %+v", again)
	}
	for _, entry := range entries {
		if string(pageBytes(t, a, entry)) != "page" {
			t.Fatal("wrong bytes")
		}
	}
}

func localZIP(t *testing.T, method uint16, descriptor, signature bool) ([]byte, []byte, int) {
	t.Helper()
	var data bytes.Buffer
	payload := bytes.Repeat([]byte("page pixels"), 100)
	fourth := 0
	for i := range 5 {
		name := fmt.Sprintf("%03d.png", i+1)
		compressed := payload
		if method == zip.Deflate {
			var buf bytes.Buffer
			w, err := flate.NewWriter(&buf, flate.DefaultCompression)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(payload)
			w.Close()
			compressed = buf.Bytes()
		}
		header := make([]byte, 30)
		binary.LittleEndian.PutUint32(header, 0x04034b50)
		binary.LittleEndian.PutUint16(header[4:], 20)
		binary.LittleEndian.PutUint16(header[8:], method)
		binary.LittleEndian.PutUint16(header[26:], uint16(len(name)))
		crc := crc32.ChecksumIEEE(payload)
		if descriptor {
			binary.LittleEndian.PutUint16(header[6:], 8)
		} else {
			binary.LittleEndian.PutUint32(header[14:], crc)
			binary.LittleEndian.PutUint32(header[18:], uint32(len(compressed)))
			binary.LittleEndian.PutUint32(header[22:], uint32(len(payload)))
		}
		data.Write(header)
		data.WriteString(name)
		if i == 3 {
			fourth = data.Len() + len(compressed)/2
		}
		data.Write(compressed)
		if descriptor {
			if signature {
				binary.Write(&data, binary.LittleEndian, uint32(0x08074b50))
			}
			binary.Write(&data, binary.LittleEndian, crc)
			binary.Write(&data, binary.LittleEndian, uint32(len(compressed)))
			binary.Write(&data, binary.LittleEndian, uint32(len(payload)))
		}
	}
	return data.Bytes(), payload, fourth
}

func TestTruncatedZIP(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		method                uint16
		descriptor, signature bool
	}{{"stored", zip.Store, false, false}, {"deflate", zip.Deflate, false, false}, {"descriptor", zip.Deflate, true, true}, {"unsigned-descriptor", zip.Deflate, true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			data, payload, end := localZIP(t, tc.method, tc.descriptor, tc.signature)
			root := saveZIP(t, data[:end])
			a := openAdapter(t, root, "pages.zip", "zip", Limits{})
			entries, caps := manifest(t, a)
			if len(entries) != 3 || !caps.Salvaged {
				t.Fatalf("entries=%+v caps=%+v", entries, caps)
			}
			a.Close()
			fresh := openAdapter(t, root, "pages.zip", "zip", Limits{})
			for i, entry := range entries {
				if entry.Locator != i || !bytes.Equal(pageBytes(t, fresh, entry), payload) {
					t.Fatalf("entry=%+v", entry)
				}
			}
			again, _ := manifest(t, fresh)
			if !reflect.DeepEqual(again, entries) {
				t.Fatal("reopen changed manifest")
			}
		})
	}
}

func TestSalvagedZIPChecksumAndLimits(t *testing.T) {
	data, _, end := localZIP(t, zip.Store, false, false)
	data[30+len("001.png")] ^= 1
	a := openAdapter(t, saveZIP(t, data[:end]), "pages.zip", "zip", Limits{})
	entries, _ := manifest(t, a)
	r, err := a.Open(context.Background(), entries[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(r)
	r.Close()
	if !errors.Is(err, zip.ErrChecksum) {
		t.Fatalf("CRC: %v", err)
	}
	_, err = New(saveZIP(t, data[:end]), "pages.zip", "zip", Limits{MaxEntries: 2})
	if !errors.Is(err, ErrLimit) {
		t.Fatalf("count: %v", err)
	}
	data, _, end = localZIP(t, zip.Deflate, true, true)
	_, err = New(saveZIP(t, data[:end]), "pages.zip", "zip", Limits{MaxEntryBytes: 8})
	if !errors.Is(err, zip.ErrFormat) {
		t.Fatalf("inflate bound: %v", err)
	}
	for _, data := range [][]byte{[]byte("garbage"), data[:20]} {
		_, err := New(saveZIP(t, data), "pages.zip", "zip", Limits{})
		if Code(err) != "corrupt" || Message(err) != "invalid ZIP archive" || !errors.Is(err, zip.ErrFormat) {
			t.Fatalf("error: %v", err)
		}
	}
}

func TestSevenAndRARSkipOversizedPages(t *testing.T) {
	for _, name := range []string{"pages-solid-on.7z", "pages-rar5-solid.rar"} {
		a := openAdapter(t, openRoot(t, "testdata"), name, filepath.Ext(name)[1:], Limits{MaxEntryBytes: 1})
		entries, caps, err := a.List(context.Background())
		if Code(err) != "unsupported_format" || len(entries) != 0 || caps.Skipped["too_large"] == 0 {
			t.Fatalf("entries=%+v caps=%+v", entries, caps)
		}
	}
}

func TestZIPLeadingSlashes(t *testing.T) {
	t.Setenv("GODEBUG", "zipinsecurepath=0")
	dir := t.TempDir()
	payload := []byte("original page bytes")
	name := makeZIP(t, dir, []string{"/001.png", "/002.png", "/003.png", "/004.png", "../x.png"}, payload)
	a := openAdapter(t, openRoot(t, dir), name, "zip", Limits{})
	entries, caps := manifest(t, a)
	if len(entries) != 4 || caps.Skipped["unsafe_path"] != 1 {
		t.Fatalf("entries=%+v caps=%+v", entries, caps)
	}
	for i, entry := range entries {
		if entry.Path != fmt.Sprintf("%03d.png", i+1) || !bytes.Equal(pageBytes(t, a, entry), payload) {
			t.Fatalf("page: %+v", entry)
		}
	}
}

func TestZIPEmptyListingReasons(t *testing.T) {
	for _, tc := range []struct {
		name          string
		names         []string
		encrypted     int
		code, message string
	}{
		{"unsafe", []string{"../1.png", "../2.png", "../3.png", "../4.png"}, 0, "unsupported_format", "all 4 image pages were skipped (unsafe_path: 4)"},
		{"encrypted", []string{"1.png", "2.png"}, 2, "encrypted_or_password_required", "all image pages are encrypted"},
		{"mostly encrypted", []string{"1.png", "2.png", "../3.png"}, 2, "encrypted_or_password_required", "all image pages are encrypted"},
		{"tied", []string{"1.png", "../2.png"}, 1, "unsupported_format", "all 2 image pages were skipped (encrypted: 1, unsafe_path: 1)"},
		{"mostly unsafe", []string{"1.png", "../2.png", "../3.png"}, 1, "unsupported_format", "all 3 image pages were skipped (encrypted: 1, unsafe_path: 2)"},
		{"extensions", []string{"secret/a.BMP", "b.bmp", "private.txt", "README", "a.csv", "a.pdf", "a.tif", "directory/"}, 0, "unsupported_format", "No supported image pages (found: 2 .bmp, 1 (none), 1 .csv, 1 .pdf, 1 .tif)"},
		{"empty", nil, 0, "unsupported_format", "No supported image pages"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var data bytes.Buffer
			w := zip.NewWriter(&data)
			for i, name := range tc.names {
				h := zip.FileHeader{Name: name}
				if i < tc.encrypted {
					h.Flags = 1
				}
				if _, err := w.CreateRaw(&h); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			a := openAdapter(t, saveZIP(t, data.Bytes()), "pages.zip", "zip", Limits{})
			entries, _, err := a.List(context.Background())
			if len(entries) != 0 || Code(err) != tc.code || Message(err) != tc.message {
				t.Fatalf("entries=%+v error=%v message=%q", entries, err, Message(err))
			}
		})
	}
}

func TestJPEGExtensions(t *testing.T) {
	dir := t.TempDir()
	name := makeZIP(t, dir, []string{"1.jfif", "2.JPE"}, []byte("jpeg bytes"))
	a := openAdapter(t, openRoot(t, dir), name, "zip", Limits{})
	entries, _ := manifest(t, a)
	if len(entries) != 2 {
		t.Fatalf("pages=%d", len(entries))
	}
	for _, entry := range entries {
		if entry.MIME != "image/jpeg" || string(pageBytes(t, a, entry)) != "jpeg bytes" {
			t.Fatalf("page: %+v", entry)
		}
	}
}

func TestNormalizeLeadingSlashes(t *testing.T) {
	for _, name := range []string{"///001.png", `\\001.png`} {
		if normal, err := normalize(name); err != nil || normal != "001.png" {
			t.Fatalf("normalize: %q %v", normal, err)
		}
	}
	for _, name := range []string{"///", "/./", "/../x.png", "/nul\x00.png"} {
		if _, err := normalize(name); err == nil {
			t.Fatal("unsafe name accepted")
		}
	}
}
