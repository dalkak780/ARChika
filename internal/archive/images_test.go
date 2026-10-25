package archive

import (
	"bytes"
	"context"
	"image/png"
	"strings"
	"testing"
)

 func TestRARImagePayloadsAndUnicodeJumpNavigation(t *testing.T) {
 for _, name := range []string{"real-pages-rar4-store.rar","real-pages-rar5-solid.rar", "real-pages-rar5-nonsolid.rar"} {
		t.Run(name, func(t *testing.T) {
			root := openRoot(t, "testdata")
			engine := openAdapter(t, root, name, "rar", Limits{})
			entries, caps := manifest(t, engine)
 if len(entries)!=16||!strings.Contains(name,"rar4")&&!strings.Contains(entries[15].Path,"한국어") {
				t.Fatalf("RAR image manifest %+v", entries)
			}
			wantSolid := "no"
			if strings.Contains(name, "-solid.") {
				wantSolid = "yes"
			}
			if caps.Solid != wantSolid {
				t.Fatalf("RAR solid capability %+v", caps)
			}
			engine.Close()
			fresh := openAdapter(t, root, name, "rar", Limits{})
			for _, ordinal := range []int{0, 1, 2, 14, 13} {
				b := pageBytes(t, fresh, entries[ordinal])
				img, e := png.Decode(bytes.NewReader(b))
				if e != nil {
					t.Fatal("decoded entry is not an original PNG", e)
				}
				r, g, blue, _ := img.At(0, 0).RGBA()
				n := ordinal + 1
				if r>>8 != uint32(n*13) || g>>8 != uint32(60+n*3) || blue>>8 != 100 {
					t.Fatalf("wrong original page %d", ordinal+1)
				}
			}
		})
	}
}
func TestRAR5EncryptedHeaderIsClassifiedWithoutPasswordSupport(t *testing.T) {
	root := openRoot(t, "testdata")
	engine, e := New(root, "real-pages-rar5-encrypted.rar", "rar", Limits{})
	if e == nil {
		defer engine.Close()
		_, _, e = engine.List(context.Background())
	}
	if Code(e) != "encrypted_or_password_required" {
		t.Fatalf("encrypted archive classification: %v (%s)", e, Code(e))
	}
}
