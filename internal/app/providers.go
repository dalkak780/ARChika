package app

// Matching order and response mappings are based on LANraragi EHentai.pm and
// Chaika.pm, and Ougi metadata-nhentai/2.0.0 (MIT, Difegue 2018/2026).
// Unlike the references, text search never automatically confirms identity.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type ProviderInput struct {
	WorkID    string          `json:"work_id"`
	Title     string          `json:"title"`
	Tags      []string        `json:"tags"`
	SourceID  string          `json:"source_id,omitempty"`
	SourceURL string          `json:"source_url,omitempty"`
	SHA1      string          `json:"first_page_sha1,omitempty"`
	LocalJSON json.RawMessage `json:"local_json,omitempty"`
}
type ProviderResult struct {
	SourceID   string            `json:"source_id"`
	SourceURL  string            `json:"source_url"`
	Fields     map[string]string `json:"fields"`
	Tags       []string          `json:"tags"`
	Confidence float64           `json:"confidence"`
	Evidence   []string          `json:"evidence"`
	Version    string            `json:"version"`
	Raw        any               `json:"raw,omitempty"`
}
type Broker func(context.Context, string, string, []byte, map[string]string) ([]byte, error)
type Provider interface {
	Lookup(context.Context, ProviderInput, Broker, map[string]any, map[string]string) ([]ProviderResult, error)
}
type builtinProvider string

var ehGallery = regexp.MustCompile(`https?://(?:e-hentai\.org|exhentai\.org)/g/([0-9]+)/([A-Za-z0-9]+)/?`)
var nhGallery = regexp.MustCompile(`(?:https?://)?nhentai\.net/g/([0-9]+)`)
var nhFilename = regexp.MustCompile(`\{([0-9]+)\}`)
var chaikaGallery = regexp.MustCompile(`https?://panda\.chaika\.moe/(gallery|archive)/([0-9]+)`)
var ehLinks = regexp.MustCompile(`https?://(?:e-hentai\.org|exhentai\.org)/g/[0-9]+/[A-Za-z0-9]+/?`)

func (p builtinProvider) Lookup(ctx context.Context, input ProviderInput, request Broker, config map[string]any, secrets map[string]string) ([]ProviderResult, error) {
	switch p {
	case "local-json":
		if len(input.LocalJSON) == 0 {
			return nil, errors.New("local JSON payload required")
		}
		var r ProviderResult
		if e := json.Unmarshal(input.LocalJSON, &r); e != nil {
			return nil, &crawlError{Stage: "parser"}
		}
		if r.SourceID == "" {
			r.SourceID = input.WorkID
		}
		r.Version = "local-json-1"
		r.Confidence = 1
		r.Evidence = []string{"explicit local JSON import"}
		return []ProviderResult{r}, nil
	case "nhentai":
		headers := credentialHeaders(secrets)
		source := input.SourceID
		if source == "" {
			if m := nhGallery.FindStringSubmatch(input.SourceURL); m != nil {
				source = m[1]
			}
		}
		if source != "" {
			b, e := request(ctx, "GET", "https://nhentai.net/api/v2/galleries/"+url.PathEscape(source), nil, headers)
			if e != nil {
				return nil, e
			}
			r, e := parseNH(b)
			if e != nil {
				return nil, e
			}
			r.Evidence = []string{"explicit provider gallery ID"}
			r.Confidence = 1
			return []ProviderResult{r}, nil
		}
		b, e := request(ctx, "GET", "https://nhentai.net/api/v2/search?query="+url.QueryEscape(input.Title), nil, headers)
		if e != nil {
			return nil, e
		}
		var reply struct {
			Result []json.RawMessage `json:"result"`
		}
		if json.Unmarshal(b, &reply) != nil {
			return nil, &crawlError{Stage: "parser"}
		}
		out := []ProviderResult{}
		for _, raw := range reply.Result[:min(5, len(reply.Result))] {
			r, e := parseNH(raw)
			if e != nil {
				continue
			}
			r.Confidence = similarity(input.Title, r.Fields["title"])
			r.Evidence = []string{"title search; human confirmation required"}
			out = append(out, r)
		}
		return out, nil
	case "ehentai":
		domain := "https://e-hentai.org"
		if enabled, _ := config["exhentai"].(bool); enabled {
			domain = "https://exhentai.org"
		}
		headers := credentialHeaders(secrets)
		source := input.SourceURL
		ids := []string{}
		if m := ehGallery.FindString(source); m != "" {
			ids = append(ids, m)
		} else {
			searchURL := domain + "/?advsearch=1&f_sfu=on&f_sft=on&f_sfl=on&f_search=" + url.QueryEscape("\""+input.Title+"\"")
			if input.SHA1 != "" {
				searchURL = domain + "/?f_shash=" + input.SHA1 + "&fs_similar=on&fs_covers=on"
			}
			b, e := request(ctx, "GET", searchURL, nil, headers)
			if e != nil {
				return nil, e
			}
			ids = ehLinks.FindAllString(string(b), 10)
			if len(ids) == 0 && input.SHA1 != "" {
				b, e = request(ctx, "GET", domain+"/?f_search="+url.QueryEscape(input.Title), nil, headers)
				if e != nil {
					return nil, e
				}
				ids = ehLinks.FindAllString(string(b), 10)
			}
		}
		seen := map[string]bool{}
		out := []ProviderResult{}
		for _, source := range ids {
			m := ehGallery.FindStringSubmatch(source)
			if m == nil || seen[m[1]] {
				continue
			}
			seen[m[1]] = true
			gid, _ := strconv.ParseInt(m[1], 10, 64)
			payload, _ := json.Marshal(map[string]any{"method": "gdata", "gidlist": [][]any{{gid, m[2]}}, "namespace": 1})
			apiHeaders := map[string]string{"Content-Type": "application/json"}
			b, e := request(ctx, "POST", "https://api.e-hentai.org/api.php", payload, apiHeaders)
			if e != nil {
				return nil, e
			}
			r, e := parseEH(b, source)
			if e != nil {
				return nil, e
			}
			r.Confidence = similarity(input.Title, r.Fields["title"])
			r.Evidence = []string{"title or optional first-page SHA-1 candidate; confirmation required"}
			if input.SourceURL != "" {
				r.Confidence = 1
				r.Evidence = []string{"explicit provider source URL"}
			}
			out = append(out, r)
			if len(out) >= 5 {
				break
			}
		}
		return out, nil
	case "chaika":
		headers := map[string]string{}
		searchURL := "https://panda.chaika.moe/jsearch/?gsp&title=" + url.QueryEscape(strings.NewReplacer("-", " ", "'", " ").Replace(strings.ToLower(input.Title))) + "&tags="
		direct := false
		if m := chaikaGallery.FindStringSubmatch(input.SourceURL); m != nil {
			searchURL = "https://panda.chaika.moe/jsearch/?" + m[1] + "=" + m[2]
			direct = true
		} else if input.SHA1 != "" {
			searchURL = "https://panda.chaika.moe/jsearch/?sha1=" + input.SHA1
		}
		b, e := request(ctx, "GET", searchURL, nil, headers)
		if e != nil {
			return nil, e
		}
		out, e := parseChaika(b)
		if e != nil {
			return nil, e
		}
		if len(out) == 0 && input.SHA1 != "" && !direct {
			b, e = request(ctx, "GET", "https://panda.chaika.moe/jsearch/?gsp&title="+url.QueryEscape(input.Title), nil, headers)
			if e != nil {
				return nil, e
			}
			out, e = parseChaika(b)
			if e != nil {
				return nil, e
			}
		}
		for i := range out {
			out[i].Confidence = similarity(input.Title, out[i].Fields["title"])
			out[i].Evidence = []string{"title or SHA-1 candidate; confirmation required"}
			if direct {
				out[i].Confidence = 1
				out[i].Evidence = []string{"explicit Chaika source URL"}
			}
		}
		return out, nil
	}
	return nil, errors.New("provider not implemented")
}
func credentialHeaders(secrets map[string]string) map[string]string {
	out := map[string]string{}
	if cookie := secrets["cookie"]; cookie != "" {
		out["Cookie"] = cookie
	}
	if token := secrets["token"]; token != "" {
		out["Authorization"] = "Bearer " + token
	}
	return out
}
func parseNH(raw []byte) (ProviderResult, error) {
	var reply struct {
		ID         json.Number                                `json:"id"`
		Title      struct{ Pretty, English, Japanese string } `json:"title"`
		Tags       []struct{ Type, Name string }              `json:"tags"`
		UploadDate json.Number                                `json:"upload_date"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.ID == "" {
		return ProviderResult{}, &crawlError{Stage: "parser"}
	}
	r := ProviderResult{SourceID: reply.ID.String(), SourceURL: "https://nhentai.net/g/" + reply.ID.String() + "/", Fields: map[string]string{"title": reply.Title.Pretty, "title_japanese": reply.Title.Japanese, "published_at": reply.UploadDate.String()}, Version: "nhentai-v2-1"}
	if r.Fields["title"] == "" {
		r.Fields["title"] = reply.Title.English
	}
	for _, tag := range reply.Tags {
		v := tag.Name
		if tag.Type != "tag" && tag.Type != "" {
			v = tag.Type + ":" + v
		}
		r.Tags = append(r.Tags, normalizeTag(v))
	}
	var safe any
	_ = json.Unmarshal(raw, &safe)
	r.Raw = safe
	r.Fields["source_url"] = r.SourceURL
	return r, nil
}
func parseEH(raw []byte, source string) (ProviderResult, error) {
	var reply struct {
		Error    string `json:"error"`
		Metadata []struct {
			GID      int64       `json:"gid"`
			Title    string      `json:"title"`
			Japanese string      `json:"title_jpn"`
			Category string      `json:"category"`
			Tags     []string    `json:"tags"`
			Uploader string      `json:"uploader"`
			Posted   json.Number `json:"posted"`
			Error    string      `json:"error"`
		} `json:"gmetadata"`
	}
	if json.Unmarshal(raw, &reply) != nil || reply.Error != "" || len(reply.Metadata) != 1 || reply.Metadata[0].Error != "" || reply.Metadata[0].GID <= 0 {
		return ProviderResult{}, &crawlError{Stage: "parser"}
	}
	m := reply.Metadata[0]
	r := ProviderResult{SourceID: strconv.FormatInt(m.GID, 10), SourceURL: source, Fields: map[string]string{"title": html.UnescapeString(m.Title), "title_japanese": html.UnescapeString(m.Japanese), "category": m.Category, "uploader": m.Uploader, "published_at": m.Posted.String(), "source_url": source}, Tags: m.Tags, Version: "ehentai-gdata-1"}
	if m.Category != "" {
		r.Tags = append(r.Tags, "category:"+normalizeTag(m.Category))
	}
	var data any
	_ = json.Unmarshal(raw, &data)
	r.Raw = data
	return r, nil
}
func parseChaika(raw []byte) ([]ProviderResult, error) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, &crawlError{Stage: "parser"}
	}
	list := []any{}
	switch v := value.(type) {
	case []any:
		list = v
	case map[string]any:
		if galleries, ok := v["galleries"].([]any); ok {
			list = galleries
		} else {
			list = append(list, v)
		}
	default:
		return nil, &crawlError{Stage: "parser"}
	}
	out := []ProviderResult{}
	for _, v := range list[:min(len(list), 5)] {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		sid := stringValue(m["gallery"])
		if sid == "" {
			sid = stringValue(m["id"])
		}
		if sid == "" {
			continue
		}
		title := stringValue(m["title"])
		r := ProviderResult{SourceID: sid, SourceURL: "https://panda.chaika.moe/gallery/" + sid + "/", Fields: map[string]string{"title": title, "title_japanese": stringValue(m["title_jpn"]), "category": stringValue(m["category"]), "published_at": stringValue(m["posted"])}, Version: "chaika-jsearch-1", Raw: m}
		r.Fields["source_url"] = r.SourceURL
		if tags, ok := m["tags"].([]any); ok {
			for _, tag := range tags {
				r.Tags = append(r.Tags, normalizeTag(stringValue(tag)))
			}
		}
		out = append(out, r)
	}
	return out, nil
}
func stringValue(v any) string {
	if v == nil {
		return ""
	}
	switch v := v.(type) {
	case string:
		return v
	case float64:
		return strconv.FormatFloat(v, 'f', 0, 64)
	case json.Number:
		return v.String()
	}
	return ""
}
func similarity(a, b string) float64 {
	aa := strings.Fields(normalizeTag(a))
	bb := map[string]bool{}
	for _, v := range strings.Fields(normalizeTag(b)) {
		bb[v] = true
	}
	if len(aa) == 0 || len(bb) == 0 {
		return 0
	}
	same := 0
	for _, v := range aa {
		if bb[v] {
			same++
		}
	}
	return min(.95, 2*float64(same)/float64(len(aa)+len(bb)))
}
func firstPageSHA1(b []byte) string { sum := sha1.Sum(b); return hex.EncodeToString(sum[:]) }
func validateResult(r ProviderResult) error {
	if r.SourceID == "" || len(r.SourceID) > 300 || len(r.SourceURL) > 2048 || len(r.Fields) > 32 || len(r.Tags) > 1000 || len(r.Evidence) > 20 || math.IsNaN(r.Confidence) || math.IsInf(r.Confidence, 0) || r.Confidence < 0 || r.Confidence > 1 {
		return errors.New("invalid provider identity or payload limits")
	}
	for k, v := range r.Fields {
		if !allowedField(k) || len(v) > 65536 {
			return fmt.Errorf("invalid provider field %q", k)
		}
	}
	for _, tag := range r.Tags {
		if len(tag) > 300 || tag == "" {
			return errors.New("invalid provider tag")
		}
	}
	return nil
}
func normalizeResult(r *ProviderResult) {
	for i, tag := range r.Tags {
		r.Tags[i] = normalizeTag(tag)
	}
	sort.Strings(r.Tags)
	r.Tags = dedupStrings(r.Tags)
	if r.Fields == nil {
		r.Fields = map[string]string{}
	}
	if r.Evidence == nil {
		r.Evidence = []string{}
	}
}
