package app

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	ar "archika/internal/archive"
)

func TestPermanentThumbnailError(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		permanent bool
	}{
		{"decode", errUnsupportedTransform, true},
		{"checksum", zip.ErrChecksum, true},
		{"unsupported archive", zip.ErrAlgorithm, true},
		{"remote corrupt", &remoteError{code: "corrupt"}, true},
		{"remote unsupported", &remoteError{code: "unsupported_format"}, true},
		{"archive corrupt", &ar.Error{Kind: "corrupt", Err: errors.New("bad entry")}, true},
		{"offline", errors.New("root_offline"), false},
		{"unavailable", errors.New("source_unavailable"), false},
		{"queue", errors.New("page work queue is full"), false},
		{"slot", errors.New("transform slot busy"), false},
		{"cancelled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, false},
		{"wrapped cancellation", &ar.Error{Kind: "corrupt", Err: context.Canceled}, false},
		{"remote IO", &remoteError{code: "io_error"}, false},
		{"limit", ar.ErrLimit, false},
		{"unknown", errors.New("worker unavailable"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, err := range []error{tc.err, fmt.Errorf("thumbnail: %w", tc.err)} {
				if got := permanentThumbnailError(err); got != tc.permanent {
					t.Fatalf("permanentThumbnailError(%v) = %v, want %v", err, got, tc.permanent)
				}
			}
		})
	}
}

func TestThumbnailFailurePersistence(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrupt=%t", corrupt), func(t *testing.T) {
			a, roots := testApp(t)
			writeZIP(t, filepath.Join(roots[0].Path, "book.cbz"))
			al := readyWork(t, a, registerWork(t, a, roots[0], "book.cbz"))
			ctx := context.Background()
			if corrupt {
				p, err := a.page(ctx, al, 0)
				if err != nil {
					t.Fatal(err)
				}
				key := fmt.Sprintf("original-page/%s/%d/%s", al.WorkID, al.Version, p.ID)
				if _, err = a.cache.Load(ctx, key, false, func(context.Context) (PageValue, error) {
					return PageValue{Bytes: []byte("invalid image"), MIME: "image/png"}, nil
				}); err != nil {
					t.Fatal(err)
				}
			} else {
				// Leave room for the thumbnail flight, but not its nested page load.
				a.cache.mu.Lock()
				for i := 0; i < 127; i++ {
					a.cache.flights[fmt.Sprintf("occupied/%d", i)] = &cacheFlight{}
				}
				a.cache.mu.Unlock()
			}
			_, _, err := a.thumbnail(ctx, User{Admin: true}, al)
			if err == nil {
				t.Fatal("expected thumbnail failure")
			}
			var failed int
			if err = a.DB.QueryRow("SELECT count(*) FROM thumbnails WHERE work_id=? AND status='failed'", al.WorkID).Scan(&failed); err != nil {
				t.Fatal(err)
			}
			if corrupt {
				if failed != 1 {
					t.Fatalf("failed rows = %d, want 1", failed)
				}
				if _, _, err = a.thumbnail(ctx, User{Admin: true}, al); !errors.Is(err, errUnsupportedTransform) {
					t.Fatalf("cached failure = %v", err)
				}
			} else {
				a.cache.mu.Lock()
				for i := 0; i < 127; i++ {
					delete(a.cache.flights, fmt.Sprintf("occupied/%d", i))
				}
				a.cache.mu.Unlock()
				if failed != 0 {
					t.Fatalf("transient failure persisted %d rows", failed)
				}
				if _, _, err = a.thumbnail(ctx, User{Admin: true}, al); err != nil {
					t.Fatalf("retry: %v", err)
				}
			}
		})
	}
}
