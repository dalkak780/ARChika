package archive

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"testing"
)

func TestListExistingFixtures(t *testing.T) {
	fixtures := map[string]string{
		"pages-rar4.rar":                                 "47470521b83a0eb211eb6260cddb0cb123759484ff0a45bffb204de90e55f171",
		"pages-rar5-nonsolid.rar":                        "e066d00705cee8403245d59d783a3a1151da4cb8576b443ed6d0b8490abf0e7f",
		"pages-rar5-solid.rar":                           "e066d00705cee8403245d59d783a3a1151da4cb8576b443ed6d0b8490abf0e7f",
		"pages-solid-off.7z":                             "04653c133cf02e5cf9e3c159706bc3587af7a9d23358d2a11dd60d33d7d475fe",
		"pages-solid-on.7z":                              "04653c133cf02e5cf9e3c159706bc3587af7a9d23358d2a11dd60d33d7d475fe",
		"real-pages-rar4-store.rar":                      "cc4c4c46c2241fe2ddecafd2454a13a5198b0716d5150c1d643a4877082c6bd6",
		"real-pages-rar5-nonsolid.rar":                   "952f77ca155e15e228cafe72d2a62700837d0aabf6f65815edb747bcf6fedd15",
		"real-pages-rar5-solid.rar":                      "ffeac4e6c55360f7af0bc29e24c63c578f97ccf847eed13e558c5333b9af3e6d",
		"test_read_format_rar.rar":                       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"test_read_format_rar5_multiple_files.rar":       "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"test_read_format_rar5_multiple_files_solid.rar": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"upstream-lzma.7z":                               "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
	}
	root := openRoot(t, "testdata")
	for name, want := range fixtures {
		t.Run(name, func(t *testing.T) {
			a := openAdapter(t, root, name, filepath.Ext(name)[1:], Limits{})
			entries, _, err := a.List(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			h := sha256.New()
			for _, entry := range entries {
				fmt.Fprintf(h, "%q %q %d %d %d %q\n", entry.Path, entry.ID, entry.Ordinal, entry.Locator, entry.Size, entry.MIME)
			}
			if got := fmt.Sprintf("%x", h.Sum(nil)); got != want {
				t.Fatalf("%s: got %s, want %s", name, got, want)
			}
		})
	}
}
