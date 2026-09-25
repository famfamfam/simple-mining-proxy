package admin

import (
	"encoding/json"
	"net/http"
	"testing"
)

// The API does not translate: errors carry a key and params for the UI and
// an English message for scripts.
func TestErrorsCarryKeys(t *testing.T) {
	e := newEnv(t)
	var body struct {
		Error   string `json:"error"`
		Key     string `json:"key"`
		Message string `json:"message"`
		Fields  map[string]struct {
			Key    string         `json:"key"`
			Params map[string]any `json:"params"`
		} `json:"fields"`
	}

	w := e.do("GET", "/api/status", "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != 401 || body.Key != "login_required" || body.Message == "" {
		t.Fatalf("unauthorized: %d %s", w.Code, w.Body)
	}

	w = e.do("POST", "/api/pools/test", `{"name":"X","addresses":[{"host":"x.example.com","port":0}],"username":"u"}`, bearer)
	body.Fields = nil
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusBadRequest {
		t.Fatalf("test unsaved config: %d %s", w.Code, w.Body)
	}
	f := body.Fields["addresses"]
	if body.Key != "pool_invalid" || f.Key != "pool_address" || f.Params["n"] != float64(1) {
		t.Fatalf("field error: %s", w.Body)
	}
}
