package app

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"testing"
)

func TestProviderResponseMappingsAndExplicitMatching(t *testing.T) {
	nh, e := parseNH([]byte(`{"id":123,"title":{"pretty":"Pretty title","japanese":"原題"},"tags":[{"type":"artist","name":"some_artist"},{"type":"tag","name":"bare_tag"}],"upload_date":12345}`))
	if e != nil {
		t.Fatal(e)
	}
	if nh.SourceURL != "https://nhentai.net/g/123/" || nh.Fields["title"] != "Pretty title" || nh.Tags[0] != "artist:some artist" || nh.Tags[1] != "bare tag" {
		t.Fatalf("nHentai metadata %+v", nh)
	}
	eh, e := parseEH([]byte(`{"gmetadata":[{"gid":42,"title":"A &amp; B","title_jpn":"原題","category":"Manga","tags":["artist:test"],"uploader":"u","posted":"100"}]}`), "https://e-hentai.org/g/42/abc/")
	if e != nil || eh.Fields["title"] != "A & B" || eh.Tags[1] != "category:manga" {
		t.Fatalf("EH response %+v %v", eh, e)
	}
	chaika, e := parseChaika([]byte(`{"galleries":[{"id":77,"title":"Title","tags":["artist:some_artist"],"title_jpn":"原題"}]}`))
	if e != nil || len(chaika) != 1 || chaika[0].SourceID != "77" || chaika[0].Tags[0] != "artist:some artist" {
		t.Fatalf("Chaika %+v %v", chaika, e)
	}
	if _, e = parseEH([]byte(`{"gmetadata":[{"error":"private"}]}`), ""); e == nil {
		t.Fatal("provider error accepted as empty successful metadata")
	}
	broker := func(ctx context.Context, method, raw string, body []byte, headers map[string]string) ([]byte, error) {
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		if u.Path != "/api/v2/galleries/123" {
			t.Fatalf("confirmed source repeated lookup: %s", u.Path)
		}
		return []byte(`{"id":123,"title":{"pretty":"Title"},"tags":[]}`), nil
	}
	results, e := builtinProvider("nhentai").Lookup(context.Background(), ProviderInput{SourceID: "123"}, broker, nil, nil)
	if e != nil || len(results) != 1 || results[0].Confidence != 1 {
		t.Fatalf("confirmed source fetch %+v %v", results, e)
	}
}
func TestNetworkDestinationAndSecretPolicies(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "::1", "10.1.1.1", "169.254.169.254", "100.64.1.1", "::ffff:192.168.1.1", "64:ff9b::a00:1", "2002:7f00:1::"} {
		if publicIP(net.ParseIP(s)) {
			t.Fatalf("SSRF destination accepted %s", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !publicIP(net.ParseIP(s)) {
			t.Fatalf("public destination rejected %s", s)
		}
	}
	u, _ := url.Parse("https://nhentai.net.evil.example/g/1")
	if allowDestination(u, []string{"nhentai.net"}) {
		t.Fatal("hostname suffix bypass")
	}
	h, e := NewCrawlerHTTP(DefaultConfig())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.Request(context.Background(), []string{"127.0.0.1"}, "GET", "https://127.0.0.1/", nil, nil); e == nil {
		t.Fatal("SSRF loopback was reachable")
	}
	raw := redactValue(map[string]any{"cookie": "secret-value", "title": "echo secret-value", "nested": map[string]any{"access_token": "secret-value"}}, map[string]string{"cookie": "secret-value"})
	b, _ := json.Marshal(raw)
	if string(b) != `{"nested":{},"title":"echo [REDACTED]"}` {
		t.Fatalf("secret redaction failed: %s", b)
	}
}
