package app

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type PluginManifest struct {
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	Version         string          `json:"version"`
	ProtocolVersion int             `json:"protocol_version"`
	Capabilities    []string        `json:"capabilities"`
	ConfigSchema    json.RawMessage `json:"config_schema"`
	Hosts           []string        `json:"hosts"`
	Permissions     []string        `json:"permissions"`
}
type externalProvider struct {
	app      *App
	record   providerRecord
	manifest PluginManifest
}
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcFailure     `json:"error,omitempty"`
}
type rpcFailure struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e externalProvider) Lookup(ctx context.Context, input ProviderInput, broker Broker, config map[string]any, secrets map[string]string) ([]ProviderResult, error) {
	if err := validatePluginConfig(e.manifest.ConfigSchema, config); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	working, err := os.MkdirTemp(os.TempDir(), "archika-plugin-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(working)
	cmd := exec.Command(exe, "plugin-exec", e.record.Executable)
	cmd.Dir = working
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "GOMEMLIMIT=256MiB", "GOMAXPROCS=2"}
	isolateProcess(cmd)
	cmd.Stderr = io.Discard
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	defer func() { killProcess(cmd); in.Close(); _ = cmd.Wait() }()
	stopped := make(chan struct{})
	defer close(stopped)
	go func() {
		select {
		case <-ctx.Done():
			cancelBytes, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "$/cancelRequest", "params": map[string]int{"id": 2}})
			cancelBytes = append(cancelBytes, '\n')
			sent := make(chan struct{})
			go func() { _, _ = in.Write(cancelBytes); close(sent) }()
			select {
			case <-sent:
			case <-time.After(10 * time.Millisecond):
			}
			killProcess(cmd)
		case <-stopped:
		}
	}()
	reader := bufio.NewReaderSize(out, 64<<10)
	frames := 0
	call := func(id int, method string, params any) (json.RawMessage, error) {
		b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if err != nil {
			return nil, err
		}
		if len(b) > 1<<20 {
			return nil, errors.New("plugin request too large")
		}
		if _, err = in.Write(append(b, '\n')); err != nil {
			return nil, err
		}
		for {
			frames++
			if frames > 128 {
				return nil, errors.New("plugin frame budget exceeded")
			}
			line, err := readLine(reader, 1<<20)
			if err != nil {
				return nil, err
			}
			var message rpcMessage
			if json.Unmarshal(line, &message) != nil || message.JSONRPC != "2.0" {
				return nil, errors.New("invalid plugin protocol frame")
			}
			if message.Method != "" {
				if message.Method != "host.http" || len(message.ID) == 0 {
					return nil, errors.New("unsupported plugin host operation")
				}
				var request struct {
					Method  string            `json:"method"`
					URL     string            `json:"url"`
					Body    string            `json:"body_base64"`
					Headers map[string]string `json:"headers"`
				}
				if json.Unmarshal(message.Params, &request) != nil {
					return nil, errors.New("invalid broker request")
				}
				if request.Method != "GET" && request.Method != "POST" {
					return nil, errors.New("unsupported broker method")
				}
				if len(message.ID) > 128 {
					return nil, errors.New("plugin request ID exceeds bound")
				}
				body, err := base64.StdEncoding.DecodeString(request.Body)
				if err != nil {
					return nil, err
				}
				bytes, err := broker(ctx, request.Method, request.URL, body, request.Headers)
				reply := rpcMessage{JSONRPC: "2.0", ID: message.ID}
				if err != nil {
					reply.Error = &rpcFailure{-32001, safeError(err)}
				} else if len(bytes) > 768*1024-1024 {
					reply.Error = &rpcFailure{-32002, "Host HTTP response exceeds plugin frame budget"}
				} else {
					reply.Result, _ = json.Marshal(map[string]any{"body_base64": base64.StdEncoding.EncodeToString(bytes)})
				}
				if err = json.NewEncoder(in).Encode(reply); err != nil {
					return nil, err
				}
				continue
			}
			expected := fmt.Sprint(id)
			if string(message.ID) != expected {
				return nil, errors.New("plugin response ID mismatch")
			}
			if message.Error != nil {
				return nil, errors.New("plugin reported a provider error")
			}
			if len(message.Result) == 0 {
				return nil, errors.New("plugin response lacks result")
			}
			return message.Result, nil
		}
	}
	handshake, err := call(1, "initialize", map[string]any{"protocol_version": 1, "capabilities": []string{"metadata"}, "host": map[string]bool{"http": true, "cancellation": true}})
	if err != nil {
		return nil, err
	}
	var hello struct {
		ProtocolVersion int      `json:"protocol_version"`
		Capabilities    []string `json:"capabilities"`
	}
	if json.Unmarshal(handshake, &hello) != nil || hello.ProtocolVersion != 1 {
		return nil, errors.New("plugin protocol negotiation failed")
	}
	metadata := false
	for _, capability := range hello.Capabilities {
		if capability == "metadata" {
			metadata = true
		}
	}
	if !metadata {
		return nil, errors.New("plugin does not negotiate metadata")
	}
	result, err := call(2, "provider.lookup", map[string]any{"input": input, "config": config, "credentials": secrets})
	if err != nil {
		return nil, err
	}
	var results []ProviderResult
	if json.Unmarshal(result, &results) != nil {
		return nil, errors.New("invalid normalized provider response")
	}
	for i := range results {
		results[i].Version = e.manifest.Version
	}
	return results, nil
}
func validateManifest(m PluginManifest) error {
	if !pluginID.MatchString(m.ID) || m.Name == "" || len(m.Name) > 100 || m.Version == "" || len(m.Version) > 80 || m.ProtocolVersion != 1 || len(m.Hosts) > 32 || len(m.Capabilities) > 8 || len(m.Permissions) > 16 || len(m.ConfigSchema) > 65536 {
		return errors.New("invalid manifest identity, protocol or limits")
	}
	if _, builtin := builtinHosts[m.ID]; builtin {
		return errors.New("plugin ID conflicts with a built-in provider")
	}
	metadata := false
	for _, capability := range m.Capabilities {
		switch capability {
		case "metadata":
			metadata = true
		case "authentication", "downloader", "automation":
		default:
			return errors.New("unknown plugin capability")
		}
	}
	if !metadata {
		return errors.New("v1 execution requires metadata capability; other families are negotiated extensions only")
	}
	for _, host := range m.Hosts {
		u, e := url.Parse("https://" + host)
		if e != nil || u.Hostname() != host || strings.ContainsAny(host, "/*:@ \\") || strings.HasSuffix(host, ".") {
			return errors.New("plugin hosts must be exact DNS names")
		}
	}
	return validateSchema(m.ConfigSchema)
}
func validateSchema(raw json.RawMessage) error {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return errors.New("invalid plugin configuration schema")
	}
	for k := range schema {
		switch k {
		case "type", "properties", "required", "additionalProperties", "title", "description":
		default:
			return fmt.Errorf("unsupported JSON Schema keyword %q", k)
		}
	}
	if t, ok := schema["type"]; ok && t != "object" {
		return errors.New("configuration schema type must be object")
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for _, value := range props {
			p, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid property schema")
			}
			for k := range p {
				switch k {
				case "type", "enum", "title", "description", "default", "minimum", "maximum", "maxLength":
				default:
					return fmt.Errorf("unsupported property keyword %q", k)
				}
			}
			switch p["type"] {
			case "string", "boolean", "number", "integer":
			default:
				return errors.New("supported config property types: string, boolean, number, integer")
			}
		}
	}
	return nil
}
func validatePluginConfig(raw json.RawMessage, config map[string]any) error {
	if len(raw) == 0 || string(raw) == "{}" {
		return nil
	}
	if e := validateSchema(raw); e != nil {
		return e
	}
	var schema struct {
		Properties map[string]struct {
			Type      string
			Enum      []any
			Minimum   *float64
			Maximum   *float64
			MaxLength *int
		} `json:"properties"`
		Required   []string `json:"required"`
		Additional *bool    `json:"additionalProperties"`
	}
	if json.Unmarshal(raw, &schema) != nil {
		return errors.New("invalid plugin schema")
	}
	for _, key := range schema.Required {
		if _, ok := config[key]; !ok {
			return fmt.Errorf("required configuration field %q", key)
		}
	}
	for key, value := range config {
		prop, ok := schema.Properties[key]
		if !ok {
			if schema.Additional != nil && !*schema.Additional {
				return fmt.Errorf("unknown configuration field %q", key)
			}
			continue
		}
		valid := false
		switch prop.Type {
		case "string":
			s, ok := value.(string)
			valid = ok && (prop.MaxLength == nil || len([]rune(s)) <= *prop.MaxLength)
		case "boolean":
			_, valid = value.(bool)
		case "number", "integer":
			n, ok := value.(float64)
			valid = ok && (prop.Type != "integer" || n == float64(int64(n))) && (prop.Minimum == nil || n >= *prop.Minimum) && (prop.Maximum == nil || n <= *prop.Maximum)
		}
		if !valid {
			return fmt.Errorf("invalid configuration value for %q", key)
		}
		if prop.Enum != nil {
			found := false
			vb, _ := json.Marshal(value)
			for _, ev := range prop.Enum {
				b, _ := json.Marshal(ev)
				if string(b) == string(vb) {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("configuration field %q is not an allowed value", key)
			}
		}
	}
	return nil
}
func (a *App) pluginsHTTP(w http.ResponseWriter, r *http.Request, u User) {
	provider := r.PathValue("id")
	if r.Method == "PUT" {
		a.providerWrite(w, r, u)
		return
	}
	if r.Method == "GET" {
		p, e := a.crawler.provider(r.Context(), provider)
		if e != nil || p.Kind != "external" {
			apiError(w, 404, "not_found", "Plugin unavailable")
			return
		}
		var manifest any
		_ = json.Unmarshal([]byte(p.Manifest), &manifest)
		jsonResponse(w, 200, manifest)
		return
	}
	if r.Method == "DELETE" {
		var in struct {
			Confirm bool `json:"confirm"`
		}
		if decodeJSON(w, r, &in) != nil || !in.Confirm {
			apiError(w, 400, "confirmation_required", "Explicit plugin removal confirmation required")
			return
		}
		_, e := a.DB.ExecContext(r.Context(), "DELETE FROM providers WHERE id=? AND kind='external'", provider)
		if e != nil {
			apiError(w, 503, "database", "Plugin removal failed")
			return
		}
		jsonResponse(w, 200, map[string]bool{"ok": true})
		return
	}
	var in struct {
		Manifest       PluginManifest `json:"manifest"`
		Executable     string         `json:"executable"`
		ConfirmTrusted bool           `json:"confirm_trusted"`
	}
	if decodeJSON(w, r, &in) != nil || !in.ConfirmTrusted {
		apiError(w, 400, "trust_required", "Third-party processes are trusted code, not a sandbox. Explicit trust confirmation required")
		return
	}
	if e := validateManifest(in.Manifest); e != nil {
		apiError(w, 400, "invalid_manifest", e.Error())
		return
	}
	base, e := filepath.EvalSymlinks(a.Config.PluginDirectory)
	if e != nil {
		apiError(w, 400, "invalid_executable", "Provision the plugin directory first")
		return
	}
	base, e = filepath.Abs(base)
	if e != nil {
		apiError(w, 400, "invalid_executable", "Invalid plugin directory")
		return
	}
	executable, e := filepath.EvalSymlinks(in.Executable)
	if e == nil {
		executable, e = filepath.Abs(executable)
	}
	if e != nil || !within(base, executable) {
		apiError(w, 400, "invalid_executable", "Executable must be inside the administrator-provisioned plugin directory")
		return
	}
	st, e := os.Stat(executable)
	if e != nil || !st.Mode().IsRegular() || st.Mode()&0111 == 0 {
		apiError(w, 400, "invalid_executable", "Plugin must be an executable regular file")
		return
	}
	manifest, _ := json.Marshal(in.Manifest)
	_, e = a.DB.ExecContext(r.Context(), "INSERT INTO providers(id,name,version,kind,manifest,executable) VALUES(?,?,?,'external',?,?)", in.Manifest.ID, in.Manifest.Name, in.Manifest.Version, string(manifest), executable)
	if e != nil {
		apiError(w, 409, "plugin_conflict", "Plugin ID already exists")
		return
	}
	jsonResponse(w, 201, map[string]bool{"ok": true})
}
