package app

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type crawlError struct {
	Stage  string
	Status int
}

func (e *crawlError) Error() string { return fmt.Sprintf("crawler %s error (%d)", e.Stage, e.Status) }

type dnsResult struct {
	ips     []net.IP
	expires time.Time
}
type CrawlerHTTP struct {
	config   Config
	mu       sync.Mutex
	dns      map[string]dnsResult
	next     map[string]time.Time
	resolver *http.Client
}

func NewCrawlerHTTP(c Config) (*CrawlerHTTP, error) {
	if c.DNSMode != "doh" && c.DNSMode != "system" {
		return nil, errors.New("dns_mode must be doh or system")
	}
	u, e := url.Parse(c.DoHURL)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("DoH URL must be an HTTPS resolver")
	}
	if c.ProxyURL != "" {
		p, e := url.Parse(c.ProxyURL)
		if e != nil || p.Hostname() == "" || (p.Scheme != "http" && p.Scheme != "https") {
			return nil, errors.New("proxy_url must be an HTTP(S) CONNECT proxy")
		}
	}
	h := &CrawlerHTTP{config: c, dns: map[string]dnsResult{}, next: map[string]time.Time{}}
	h.resolver = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, e
		}
		ips, e := h.bootstrap(ctx, host)
		if e != nil {
			return nil, e
		}
		return h.dial(ctx, ips[0], port)
	}, MaxConnsPerHost: 2, MaxIdleConns: 2, MaxResponseHeaderBytes: 16 << 10}, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("DoH redirects are disabled") }}
	return h, nil
}

var blockedNetworks = func() []netip.Prefix {
	out := []netip.Prefix{}
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "224.0.0.0/3", "::/128", "::1/128", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23", "2002::/16", "fc00::/7", "fe80::/10", "ff00::/8"} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

func publicIP(ip net.IP) bool {
	p, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	p = p.Unmap()
	if !p.IsGlobalUnicast() || p.IsPrivate() {
		return false
	}
	for _, b := range blockedNetworks {
		if b.Contains(p) {
			return false
		}
	}
	return true
}
func (h *CrawlerHTTP) bootstrap(ctx context.Context, host string) ([]net.IP, error) {
	if host == "cloudflare-dns.com" {
		return []net.IP{net.ParseIP("1.1.1.1"), net.ParseIP("1.0.0.1")}, nil
	}
	ips, e := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if e != nil {
		return nil, &crawlError{Stage: "dns"}
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, &crawlError{Stage: "destination"}
		}
	}
	if len(ips) == 0 {
		return nil, &crawlError{Stage: "dns"}
	}
	return ips, nil
}
func (h *CrawlerHTTP) resolve(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		if !publicIP(ip) {
			return nil, &crawlError{Stage: "destination"}
		}
		return []net.IP{ip}, nil
	}
	h.mu.Lock()
	cached := h.dns[host]
	h.mu.Unlock()
	if time.Now().Before(cached.expires) {
		return cached.ips, nil
	}
	var ips []net.IP
	if h.config.DNSMode == "system" {
		var e error
		ips, e = h.bootstrap(ctx, host)
		if e != nil {
			return nil, e
		}
	} else {
		for _, typ := range []string{"A", "AAAA"} {
			u, _ := url.Parse(h.config.DoHURL)
			q := u.Query()
			q.Set("name", host)
			q.Set("type", typ)
			u.RawQuery = q.Encode()
			req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
			req.Header.Set("Accept", "application/dns-json")
			res, e := h.resolver.Do(req)
			if e != nil {
				return nil, &crawlError{Stage: "dns"}
			}
			b, e := io.ReadAll(io.LimitReader(res.Body, 65537))
			res.Body.Close()
			if e != nil || len(b) > 65536 || res.StatusCode != 200 {
				return nil, &crawlError{Stage: "dns", Status: res.StatusCode}
			}
			var reply struct {
				Status int
				Answer []struct {
					Type int
					Data string
					TTL  int
				}
			}
			if json.Unmarshal(b, &reply) != nil || reply.Status != 0 {
				return nil, &crawlError{Stage: "dns"}
			}
			for _, answer := range reply.Answer {
				if answer.Type == 1 || answer.Type == 28 {
					if ip := net.ParseIP(answer.Data); ip != nil {
						ips = append(ips, ip)
					}
				}
			}
		}
	}
	if len(ips) == 0 {
		return nil, &crawlError{Stage: "dns"}
	}
	for _, ip := range ips {
		if !publicIP(ip) {
			return nil, &crawlError{Stage: "destination"}
		}
	}
	h.mu.Lock()
	if len(h.dns) >= 4096 {
		clear(h.dns)
	}
	h.dns[host] = dnsResult{ips, time.Now().Add(60 * time.Second)}
	h.mu.Unlock()
	return ips, nil
}
func (h *CrawlerHTTP) dial(ctx context.Context, ip net.IP, port string) (net.Conn, error) {
	target := net.JoinHostPort(ip.String(), port)
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	if h.config.ProxyURL == "" {
		return d.DialContext(ctx, "tcp", target)
	}
	proxy, _ := url.Parse(h.config.ProxyURL)
	pp := proxy.Port()
	if pp == "" {
		pp = "80"
		if proxy.Scheme == "https" {
			pp = "443"
		}
	}
	conn, e := d.DialContext(ctx, "tcp", net.JoinHostPort(proxy.Hostname(), pp))
	if e != nil {
		return nil, e
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if proxy.Scheme == "https" {
		tlsConn := tls.Client(conn, &tls.Config{ServerName: proxy.Hostname(), MinVersion: tls.VersionTLS12})
		if e = tlsConn.HandshakeContext(ctx); e != nil {
			conn.Close()
			return nil, e
		}
		conn = tlsConn
	}
	var b strings.Builder
	fmt.Fprintf(&b, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
	if proxy.User != nil {
		password, _ := proxy.User.Password()
		b.WriteString("Proxy-Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(proxy.User.Username()+":"+password)) + "\r\n")
	}
	b.WriteString("\r\n")
	if _, e = io.WriteString(conn, b.String()); e != nil {
		conn.Close()
		return nil, e
	}
	reader := bufio.NewReader(io.LimitReader(conn, 16<<10))
	response, e := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if e != nil || response.StatusCode != 200 {
		conn.Close()
		return nil, &crawlError{Stage: "proxy"}
	}
	if reader.Buffered() != 0 {
		conn.Close()
		return nil, errors.New("unexpected proxy payload")
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}
func allowDestination(u *url.URL, hosts []string) bool {
	if u.Scheme != "https" || u.User != nil || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	for _, host := range hosts {
		if strings.EqualFold(u.Hostname(), host) {
			return true
		}
	}
	return false
}
func (h *CrawlerHTTP) client(hosts []string) *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(addr)
		if e != nil {
			return nil, e
		}
		ips, e := h.resolve(ctx, host)
		if e != nil {
			return nil, e
		}
		var last error
		for _, ip := range ips {
			conn, e := h.dial(ctx, ip, port)
			if e == nil {
				return conn, nil
			}
			last = e
		}
		return nil, last
	}, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, MaxConnsPerHost: 2, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second, MaxResponseHeaderBytes: 32 << 10}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 || !allowDestination(req.URL, hosts) {
			return &crawlError{Stage: "redirect"}
		}
		if req.URL.Host != via[0].URL.Host {
			req.Header.Del("Cookie")
			req.Header.Del("Authorization")
		}
		return nil
	}}
}
func (h *CrawlerHTTP) Request(ctx context.Context, hosts []string, method, rawURL string, body []byte, headers map[string]string) ([]byte, error) {
	u, e := url.Parse(rawURL)
	if e != nil || !allowDestination(u, hosts) {
		return nil, &crawlError{Stage: "destination"}
	}
	if len(body) > 1<<20 {
		return nil, &crawlError{Stage: "request_budget"}
	}
	h.mu.Lock()
	at := h.next[u.Hostname()]
	if at.Before(time.Now()) {
		at = time.Now()
	}
	h.next[u.Hostname()] = at.Add(4 * time.Second)
	h.mu.Unlock()
	timer := time.NewTimer(time.Until(at))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
	}
	client := h.client(hosts)
	defer client.CloseIdleConnections()
	for attempt := range 3 {
		req, e := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if e != nil {
			return nil, e
		}
		req.Header.Set("User-Agent", "ARChika/1 (+self-hosted metadata client)")
		req.Header.Set("Accept", "application/json,text/html;q=0.9")
		for k, v := range headers {
			if strings.EqualFold(k, "Host") || strings.ContainsAny(v, "\r\n") {
				return nil, &crawlError{Stage: "header"}
			}
			req.Header.Set(k, v)
		}
		response, e := client.Do(req)
		if e != nil {
			var ce *crawlError
			if errors.As(e, &ce) {
				return nil, ce
			}
			return nil, &crawlError{Stage: "transport"}
		}
		b, e := io.ReadAll(io.LimitReader(response.Body, 4<<20+1))
		response.Body.Close()
		if e != nil || len(b) > 4<<20 {
			return nil, &crawlError{Stage: "body_limit"}
		}
		if (response.StatusCode == 429 || response.StatusCode >= 500) && attempt < 2 {
			delay := time.Duration(1<<attempt) * time.Second
			if n, e := strconv.Atoi(response.Header.Get("Retry-After")); e == nil && n > 0 {
				delay = time.Duration(min(n, 8)) * time.Second
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			continue
		}
		if response.StatusCode == 401 {
			return nil, &crawlError{Stage: "credentials", Status: response.StatusCode}
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil, &crawlError{Stage: "http", Status: response.StatusCode}
		}
		return b, nil
	}
	return nil, &crawlError{Stage: "http"}
}
