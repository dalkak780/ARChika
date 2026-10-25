package app

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	ar "archika/internal/archive"
)

func TestIndexMigrationAndInteractiveRetry(t *testing.T) {
	a, roots := testApp(t)
	ctx := context.Background()
	works := map[string]string{}
	for _, code := range []string{"corrupt", "encrypted_or_password_required", "unknown", "io_error", "unsupported_format", "ready"} {
		name := code + ".cbz"
		writeZIP(t, filepath.Join(roots[0].Path, name))
		al := readyWork(t, a, registerWork(t, a, roots[0], name))
		works[code] = al.WorkID
		if code != "ready" {
			if _, err := a.DB.Exec("DELETE FROM pages WHERE work_id=?", al.WorkID); err != nil {
				t.Fatal(err)
			}
			if _, err := a.DB.Exec("UPDATE works SET index_status='error',error_code=?,error_message='old failure' WHERE id=?", code, al.WorkID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := a.DB.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for code, work := range works {
		al, err := a.AuthorizedAlias(ctx, User{Admin: true}, work)
		if err != nil {
			t.Fatal(err)
		}
		switch code {
		case "corrupt", "encrypted_or_password_required", "unknown":
			if al.IndexStatus != "stale" || al.ErrorCode != "" || al.ErrorMessage != "" {
				t.Fatalf("upgrade: %+v", al)
			}
		case "ready":
			if al.IndexStatus != "ready" || al.Version != 1 {
				t.Fatalf("ready work changed: %+v", al)
			}
		default:
			if al.IndexStatus != "error" || al.ErrorCode != code {
				t.Fatalf("unrelated error changed: %+v", al)
			}
		}
		if code == "unsupported_format" {
			if err = a.ensureIndex(ctx, al); !errors.Is(err, errIndexFailed) {
				t.Fatalf("sticky error: %v", err)
			}
			continue
		}
		if code == "io_error" {
			if err = a.ensureIndex(context.WithValue(ctx, backgroundKey{}, true), al); !errors.Is(err, errIndexFailed) {
				t.Fatalf("background retry: %v", err)
			}
		}
		if err = a.ensureIndex(ctx, al); err != nil {
			t.Fatalf("%s: %v", code, err)
		}
		pages, err := a.pageList(ctx, al)
		if err != nil || len(pages) != 3 {
			t.Fatalf("%s: pages=%v err=%v", code, pages, err)
		}
	}
	if _, err := a.DB.Exec("UPDATE works SET index_status='error',error_code='corrupt',error_message='new failure' WHERE id=?", works["corrupt"]); err != nil {
		t.Fatal(err)
	}
	db, err = OpenDB(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	al, err := a.AuthorizedAlias(ctx, User{Admin: true}, works["corrupt"])
	if err != nil || al.IndexStatus != "error" || al.ErrorMessage != "new failure" {
		t.Fatalf("migration ran twice: %+v %v", al, err)
	}
}

func TestIndexFailureMessageThroughHelperAndAPI(t *testing.T) {
	a, roots := testApp(t)
	ctx := context.Background()
	for _, tc := range []struct{ name, message, code string }{
		{"garbage.cbz", "invalid ZIP archive", "corrupt"},
		{"empty.cbz", "No supported image pages", "unsupported_format"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := []byte("not a zip archive")
			if tc.name == "empty.cbz" {
				var buf bytes.Buffer
				w := zip.NewWriter(&buf)
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				data = buf.Bytes()
			}
			if err := os.WriteFile(filepath.Join(roots[0].Path, tc.name), data, 0600); err != nil {
				t.Fatal(err)
			}
			work := registerWork(t, a, roots[0], tc.name)
			if _, err := a.DB.Exec("UPDATE works SET index_status='pending' WHERE id=?", work); err != nil {
				t.Fatal(err)
			}
			al, err := a.AuthorizedAlias(ctx, User{Admin: true}, work)
			if err != nil {
				t.Fatal(err)
			}
			if err = a.ensureIndex(ctx, al); err == nil {
				t.Fatal("invalid work indexed")
			}
			al, err = a.AuthorizedAlias(ctx, User{Admin: true}, work)
			if err != nil || al.ErrorMessage != tc.message || al.ErrorCode != tc.code {
				t.Fatalf("failure: %+v %v", al, err)
			}
			req := httptest.NewRequest("GET", "/api/works/"+work, nil)
			req.SetPathValue("id", work)
			res := httptest.NewRecorder()
			a.workDetail(res, req, User{Admin: true})
			var detail struct {
				Work  Alias                          `json:"work"`
				Error struct{ Code, Message string } `json:"error"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if res.Code != 422 || detail.Error.Code != "index_failed" || detail.Error.Message != tc.message || detail.Work.ErrorMessage != tc.message {
				t.Fatalf("detail: %d %s", res.Code, res.Body.String())
			}
			res = httptest.NewRecorder()
			a.works(res, httptest.NewRequest("GET", "/api/works", nil), User{Admin: true})
			if res.Code != 200 || !strings.Contains(res.Body.String(), `"error_message":"`+tc.message+`"`) {
				t.Fatalf("listing: %d %s", res.Code, res.Body.String())
			}
		})
	}
}

func TestIndexMessagesExcludeFilesystemPaths(t *testing.T) {
	_, roots := testApp(t)
	root, err := os.OpenRoot(roots[0].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	_, err = newRemoteAdapter(context.Background(), root, "missing.cbz", "zip", ar.Limits{MaxEntryBytes: 1024, MaxEntries: 100})
	if decodeErrorCode(err) != "io_error" || !strings.HasPrefix(indexError(err), "operation failed") || strings.Contains(indexError(err), roots[0].Path) {
		t.Fatalf("remote error: %v", err)
	}
	local := &fs.PathError{Op: "open", Path: "/host/private/source.cbz", Err: fs.ErrPermission}
	if strings.Contains(indexError(local), local.Path) || !strings.HasPrefix(indexError(local), "operation failed") {
		t.Fatalf("local error leaked: %s", indexError(local))
	}
	writeZIP(t, filepath.Join(roots[0].Path, "limit.cbz"))
	adapter, err := newRemoteAdapter(context.Background(), root, "limit.cbz", "zip", ar.Limits{MaxEntryBytes: 1024, MaxEntries: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer adapter.Close()
	_, _, err = adapter.List(context.Background())
	if indexError(err) != "archive entry count" {
		t.Fatalf("limit message: %v / %s", err, indexError(err))
	}
}

func TestRecoveredZIPPagesThroughHelper(t *testing.T) {
	a, roots := testApp(t)
	pixels := testPNG(t)
	type fixture struct {
		name      string
		names     []string
		method    uint16
		truncated bool
		pages     int
		skipped   string
		first     string
		work      string
	}
	fixtures := []fixture{
		{name: "cp949", names: []string{"\xb0\xa101.png", "\xb3\xaa02.png", "\xb4\xd903.png"}, pages: 3, first: "가01.png"},
		{name: "sjis", names: []string{"\x82\xa001.png", "\x82\xa202.png", "\x82\xa403.png"}, pages: 3, first: "あ01.png"},
		{name: "colon", names: []string{"Re:Zero/001.png", "Re:Zero/002.png", "Re:Zero/003.png"}, pages: 3, first: "Re:Zero/001.png"},
		{name: "duplicate", names: []string{"001.png", "002.png", "002.png"}, pages: 2, skipped: "duplicate"},
		{name: "encrypted-readme", names: []string{"readme.txt", "001.png", "002.png", "003.png"}, pages: 3},
		{name: "unsafe", names: []string{"../evil.png", "001.png", "002.png"}, pages: 2, skipped: "unsafe_path"},
		{name: "stored", names: []string{"001.png", "002.png", "003.png", "004.png", "005.png"}, truncated: true, pages: 3},
		{name: "deflated", names: []string{"001.png", "002.png", "003.png", "004.png", "005.png"}, method: zip.Deflate, truncated: true, pages: 3},
		{name: "garbage"},
	}
	for i := range fixtures {
		f := &fixtures[i]
		data := []byte("random bytes")
		if f.pages > 0 {
			var buf bytes.Buffer
			writer := zip.NewWriter(&buf)
			for _, name := range f.names {
				compressed := pixels
				if f.method == zip.Deflate {
					var packed bytes.Buffer
					w, err := flate.NewWriter(&packed, flate.DefaultCompression)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = w.Write(pixels); err != nil {
						t.Fatal(err)
					}
					if err = w.Close(); err != nil {
						t.Fatal(err)
					}
					compressed = packed.Bytes()
				}
				header := zip.FileHeader{Name: name, NonUTF8: true, Method: f.method, CRC32: crc32.ChecksumIEEE(pixels), CompressedSize64: uint64(len(compressed)), UncompressedSize64: uint64(len(pixels))}
				if name == "readme.txt" {
					header.Flags = 1
				}
				w, err := writer.CreateRaw(&header)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = w.Write(compressed); err != nil {
					t.Fatal(err)
				}
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			data = buf.Bytes()
			if f.truncated {
				z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
				if err != nil {
					t.Fatal(err)
				}
				offset, err := z.File[3].DataOffset()
				if err != nil {
					t.Fatal(err)
				}
				data = data[:offset+int64(z.File[3].CompressedSize64)/2]
			}
		}
		name := f.name + ".cbz"
		if err := os.WriteFile(filepath.Join(roots[0].Path, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		f.work = registerWork(t, a, roots[0], name)
		if _, err := a.DB.Exec("UPDATE works SET index_status='error',error_code='corrupt',error_message='old failure' WHERE id=?", f.work); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.DB.Exec("PRAGMA user_version=3"); err != nil {
		t.Fatal(err)
	}
	db, err := OpenDB(a.Config)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	for _, f := range fixtures {
		t.Run(f.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/works/"+f.work, nil)
			req.SetPathValue("id", f.work)
			res := httptest.NewRecorder()
			a.workDetail(res, req, User{Admin: true})
			if f.pages == 0 {
				if res.Code != 422 || !strings.Contains(res.Body.String(), `"error_message":"invalid ZIP archive"`) || !strings.Contains(res.Body.String(), `"error_code":"corrupt"`) {
					t.Fatalf("garbage: %d %s", res.Code, res.Body.String())
				}
				return
			}
			var detail struct {
				Pages        []ar.Entry      `json:"pages"`
				Capabilities ar.Capabilities `json:"capabilities"`
			}
			if res.Code != 200 {
				t.Fatalf("manifest: %d %s", res.Code, res.Body.String())
			}
			if err := json.Unmarshal(res.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if len(detail.Pages) != f.pages || detail.Capabilities.Salvaged != f.truncated || f.skipped != "" && detail.Capabilities.Skipped[f.skipped] != 1 {
				t.Fatalf("manifest: %+v", detail)
			}
			if f.first != "" && detail.Pages[0].Path != f.first {
				t.Fatalf("name: %s", detail.Pages[0].Path)
			}
			for i := range detail.Pages {
				req := httptest.NewRequest("GET", fmt.Sprintf("/api/works/%s/pages/%d", f.work, i), nil)
				req.SetPathValue("id", f.work)
				req.SetPathValue("page", strconv.Itoa(i))
				res := httptest.NewRecorder()
				a.pageHTTP(res, req, User{Admin: true})
				if res.Code != 200 || !bytes.Equal(res.Body.Bytes(), pixels) {
					t.Fatalf("page %d: status=%d bytes=%d", i, res.Code, res.Body.Len())
				}
			}
		})
	}
}
