package app

import "testing"

func TestProviderSourceRequiresExactAuthorityAndGalleryPath(t *testing.T) {
	for _, tc := range []struct{ provider, url, id string }{{"nhentai", "https://nhentai.net/g/123/?p=1", "123"}, {"ehentai", "https://exhentai.org/g/42/abc123/", "42"}, {"chaika", "panda.chaika.moe/archive/77", "77"}} {
		id, canonical := canonicalSource(tc.provider, tc.url)
		if id != tc.id || canonical == "" {
			t.Fatalf("authoritative source rejected %s", tc.url)
		}
	}
	for _, url := range []string{"https://evilnhentai.net/g/123/", "https://evil.example/?url=https://nhentai.net/g/123/", "https://nhentai.net/g/123/other", "https://user:secret@nhentai.net/g/123/", "https://nhentai.net:8443/g/123/"} {
		if id, _ := canonicalSource("nhentai", url); id != "" {
			t.Fatalf("untrusted URL became an authoritative ID: %s", url)
		}
	}
}
