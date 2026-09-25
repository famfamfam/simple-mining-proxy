// Package stratum implements just enough of Stratum V1 for a relay: line
// framing, a JSON-RPC envelope and rewriting of worker logins.
package stratum

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Message is a JSON-RPC envelope. Params, Result, Error and ID are kept raw
// so that an unchanged message can be forwarded byte for byte and an id is
// echoed exactly as it arrived (number or string).
type Message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

var ErrNotObject = errors.New("stratum: message is not a JSON object")

func Parse(line []byte) (*Message, error) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] != '{' {
		return nil, ErrNotObject
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// IDKey returns the id as a map key, or "" for notifications. A number and
// the same number as a string ("7") give the same key: some pools echo a
// numeric request id back as a string, and the answer must still be matched.
func (m *Message) IDKey() string {
	if IsNull(m.ID) {
		return ""
	}
	id := string(bytes.TrimSpace(m.ID))
	if n := len(id); n > 2 && id[0] == '"' && id[n-1] == '"' && isDigits(id[1:n-1]) {
		return id[1 : n-1]
	}
	return id
}

func isDigits(s string) bool {
	if len(s) > 18 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func IsNull(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) == 0 || string(raw) == "null"
}

// ResultTrue reports whether a result is literally true.
func ResultTrue(raw json.RawMessage) bool {
	return string(bytes.TrimSpace(raw)) == "true"
}

// ParamsArray splits params into raw elements.
func ParamsArray(raw json.RawMessage) ([]json.RawMessage, error) {
	var params []json.RawMessage
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	return params, nil
}

// StringAt returns params[i] if it is a string.
func StringAt(params []json.RawMessage, i int) (string, bool) {
	if i >= len(params) {
		return "", false
	}
	var s string
	if err := json.Unmarshal(params[i], &s); err != nil {
		return "", false
	}
	return s, true
}

// NumberAt returns params[i] if it is a number (or a numeric string).
func NumberAt(params []json.RawMessage, i int) (float64, bool) {
	if i >= len(params) {
		return 0, false
	}
	finite := func(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }
	var f float64
	if err := json.Unmarshal(params[i], &f); err == nil && finite(f) {
		return f, true
	}
	var s string
	if err := json.Unmarshal(params[i], &s); err == nil {
		if f, err := strconv.ParseFloat(s, 64); err == nil && finite(f) {
			return f, true
		}
	}
	return 0, false
}

func EncodeString(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// ReplaceParams re-serializes line with new params. Every other field of the
// object (id, method, jsonrpc, unknown extensions) is kept as raw JSON.
func ReplaceParams(line []byte, params []json.RawMessage) ([]byte, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSpace(line), &obj); err != nil {
		return nil, err
	}
	p, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	obj["params"] = p
	out, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ErrorReason extracts a human-readable reject reason from a JSON-RPC error,
// which pools send as [code, "message", data] or {"code":..,"message":..}.
func ErrorReason(raw json.RawMessage) string {
	if IsNull(raw) {
		return ""
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) == nil {
		if s, ok := StringAt(arr, 1); ok {
			return s
		}
		if len(arr) > 0 {
			return string(arr[0])
		}
	}
	var obj struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Message != "" {
		return obj.Message
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	if len(raw) > 100 {
		raw = raw[:100]
	}
	return fmt.Sprintf("%s", raw)
}
