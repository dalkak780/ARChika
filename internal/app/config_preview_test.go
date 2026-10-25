package app

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestConfigPreviewListsOnlyDifferences(t *testing.T) {
	a, _ := testApp(t)
	preview := func() []map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		a.configPreview(w, httptest.NewRequest("GET", "/api/admin/config/preview", nil), User{})
		var out struct {
			Changes []map[string]any `json:"changes"`
		}
		if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
			t.Fatal(e)
		}
		return out.Changes
	}
	if changes := preview(); len(changes) != 0 {
		t.Fatalf("unchanged config reported: %v", changes)
	}
	a.Config.Roots[0].Name = "Renamed"
	a.Config.Roots = append(a.Config.Roots, RootConfig{Key: "extra", Name: "Extra", Path: a.Config.Roots[1].Path})
	changes := preview()
	if len(changes) != 2 || changes[0]["action"] != "update" || changes[1]["action"] != "add" {
		t.Fatalf("changes: %v", changes)
	}
}
