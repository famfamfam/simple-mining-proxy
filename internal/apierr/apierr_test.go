package apierr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTemplate(t *testing.T) {
	reason := M("addr_port", "port must be 1–65535")
	m := M("pool_address", "address {n} ({address}): {reason}", "n", 2, "address", "x:0", "reason", reason)
	if m.Text != "address 2 (x:0): port must be 1–65535" {
		t.Fatalf("text: %q", m.Text)
	}
	if m.Params["n"] != 2 || m.Params["reason"].(Msg).Key != "addr_port" {
		t.Fatalf("params: %+v", m.Params)
	}
}

func TestErrorJSON(t *testing.T) {
	e := Validation(M("pool_invalid", "invalid pool fields"), map[string]Msg{
		"name": M("pool_name", "required"),
	})
	b, _ := json.Marshal(e)
	for _, want := range []string{`"error":"validation"`, `"key":"pool_invalid"`, `"message":"invalid pool fields"`, `"fields":{"name":{"key":"pool_name"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing in %s", want, b)
		}
	}
	if e.Error() != "invalid pool fields" {
		t.Fatalf("Error() = %q", e.Error())
	}
}
