package app

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func pluginFixture() {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	enc := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				Input       ProviderInput     `json:"input"`
				Credentials map[string]string `json:"credentials"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if request.Method == "initialize" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"protocol_version": 1, "capabilities": []string{"metadata"}}})
			continue
		}
		if request.Method == "provider.lookup" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 100, "method": "host.http", "params": map[string]any{"method": "GET", "url": "https://127.0.0.1/", "body_base64": ""}})
			if !scanner.Scan() {
				os.Exit(4)
			}
			var reply rpcMessage
			if json.Unmarshal(scanner.Bytes(), &reply) != nil || reply.Error == nil {
				os.Exit(5)
			}
			r := ProviderResult{SourceID: request.Params.Input.SourceID, SourceURL: request.Params.Input.SourceURL, Fields: map[string]string{"title": "Remote title", "description": request.Params.Credentials["token"]}, Tags: []string{"artist:fixture"}, Confidence: 1, Evidence: []string{"confirmed source"}, Version: "1", Raw: map[string]any{"echo": request.Params.Credentials["token"]}}
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": []ProviderResult{r}})
			return
		}
		os.Exit(6)
	}
}
func TestExternalPluginBrokerAndCredentialIsolation(t *testing.T) {
	a, roots := testApp(t)
	root := roots[1]
	writeZIP(t, filepath.Join(root.Path, "Filename_[verbatim].zip"))
	work := registerWork(t, a, root, "Filename_[verbatim].zip")
	readyWork(t, a, work)
	admin := userForRoot(t, a, root.ID)
	if _, e := a.DB.Exec("UPDATE users SET admin=1 WHERE id=?", admin.ID); e != nil {
		t.Fatal(e)
	}
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	dir := t.TempDir()
	fixture := filepath.Join(dir, "metadata-fixture")
	if e = os.Link(exe, fixture); e != nil {
		t.Fatal(e)
	}
	manifest := PluginManifest{ID: "fixture", Name: "Fixture", Version: "1", ProtocolVersion: 1, Capabilities: []string{"metadata"}, Hosts: []string{"example.org"}, ConfigSchema: json.RawMessage(`{}`)}
	b, _ := json.Marshal(manifest)
	if _, e = a.DB.Exec("INSERT INTO providers(id,name,version,kind,manifest,executable) VALUES('fixture','Fixture','1','external',?,?)", string(b), fixture); e != nil {
		t.Fatal(e)
	}
	if _, e = a.DB.Exec("INSERT INTO provider_links VALUES(?,?,?,?,?)", work, "fixture", "known", "https://example.org/work/known", now()); e != nil {
		t.Fatal(e)
	}
	block, e := aes.NewCipher(make([]byte, 32))
	if e != nil {
		t.Fatal(e)
	}
	a.crawler.aead, e = cipher.NewGCM(block)
	if e != nil {
		t.Fatal(e)
	}
	secret := "fixture-secret-not-for-snapshots"
	settings, _ := json.Marshal(map[string]any{"credentials": map[string]string{"token": secret}})
	request := httptest.NewRequest("PUT", "/api/admin/providers/fixture", strings.NewReader(string(settings)))
	request.Header.Set("Content-Type", "application/json")
	request.SetPathValue("id", "fixture")
	response := httptest.NewRecorder()
	a.providerWrite(response, request, User{ID: admin.ID, Admin: true})
	if response.Code != 200 {
		t.Fatal("credential settings", response.Body.String())
	}
	var ciphertext []byte
	_ = a.DB.QueryRow("SELECT ciphertext FROM provider_credentials WHERE provider='fixture'").Scan(&ciphertext)
	if strings.Contains(string(ciphertext), secret) {
		t.Fatal("credentials stored without encryption")
	}
	if e = a.crawler.RunJob(context.Background(), Job{ID: "direct-fixture", Type: "crawl", Provider: "fixture", WorkID: work, RootID: root.ID, Actor: admin.ID, Origin: "manual", Payload: `{}`}); e != nil {
		t.Fatal(e)
	}
	metadata, e := a.crawler.Metadata(context.Background(), User{Admin: true}, work)
	if e != nil {
		t.Fatal(e)
	}
	out, _ := json.Marshal(metadata)
	if strings.Contains(string(out), secret) || !strings.Contains(string(out), "[REDACTED]") {
		t.Fatalf("secret escaped provider normalization: %s", out)
	}
	var raw string
	_ = a.DB.QueryRow("SELECT raw FROM provider_snapshots WHERE work_id=? AND provider='fixture'", work).Scan(&raw)
	if strings.Contains(raw, secret) {
		t.Fatal("raw provider snapshot exposed credential")
	}
	credentials, e := a.crawler.credentials(context.Background(), "fixture")
	if e != nil || credentials["token"] != secret {
		t.Fatal("provider credential roundtrip failed")
	}
	if _, e = a.DB.Exec("UPDATE providers SET enabled=0 WHERE id='fixture'"); e != nil {
		t.Fatal(e)
	}
	if e = a.crawler.RunJob(context.Background(), Job{Provider: "fixture", WorkID: work, Actor: admin.ID, Payload: `{}`}); e == nil {
		t.Fatal("disabled plugin executed")
	}
	if !strings.Contains(raw, "[REDACTED]") {
		t.Fatal("raw payload redaction missing")
	}
}
