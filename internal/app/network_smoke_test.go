package app

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDoHAndPinnedProxySmoke(t *testing.T) {
	if os.Getenv("ARCHIKA_NETWORK") != "1" {
		t.Skip("set ARCHIKA_NETWORK=1 to exercise public DoH and proxy egress")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	c := DefaultConfig()
	direct, e := NewCrawlerHTTP(c)
	if e != nil {
		t.Fatal(e)
	}
	ips, e := direct.resolve(ctx, "example.org")
	if e != nil {
		t.Fatal("DoH resolution", e)
	}
	if len(ips) == 0 {
		t.Fatal("DoH returned no addresses")
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			t.Fatal("unapproved resolver address", ip)
		}
	}
	body, e := direct.Request(ctx, []string{"example.org"}, "GET", "https://example.org/", nil, nil)
	if e != nil || !strings.Contains(string(body), "Example Domain") {
		t.Fatal("direct DoH HTTPS", e)
	}
	var mu sync.Mutex
	targets := []string{}
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "CONNECT" {
			w.WriteHeader(405)
			return
		}
		host, port, e := net.SplitHostPort(r.Host)
		if e != nil || port != "443" || !publicIP(net.ParseIP(host)) {
			w.WriteHeader(403)
			return
		}
		mu.Lock()
		targets = append(targets, r.Host)
		mu.Unlock()
		remote, e := net.DialTimeout("tcp", r.Host, 10*time.Second)
		if e != nil {
			w.WriteHeader(502)
			return
		}
		conn, _, e := w.(http.Hijacker).Hijack()
		if e != nil {
			remote.Close()
			return
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
		go func() { _, _ = io.Copy(remote, conn); remote.Close() }()
		_, _ = io.Copy(conn, remote)
		conn.Close()
	}))
	defer proxy.Close()
	c.ProxyURL = proxy.URL
	routed, e := NewCrawlerHTTP(c)
	if e != nil {
		t.Fatal(e)
	}
	body, e = routed.Request(ctx, []string{"example.org"}, "GET", "https://example.org/", nil, nil)
	if e != nil || !strings.Contains(string(body), "Example Domain") {
		t.Fatal("pinned proxy HTTPS", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(targets) < 2 {
		t.Fatalf("resolver or destination bypassed proxy: %v", targets)
	}
	t.Logf("DoH addresses=%d; direct and proxy HTTPS verified; %d IP-pinned CONNECT requests", len(ips), len(targets))
}
