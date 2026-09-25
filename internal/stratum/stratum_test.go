package stratum

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRewriteLogin(t *testing.T) {
	tests := []struct {
		login, tmpl string
		want        string
	}{
		{"farm.S21-0042", "accountA.{worker}", "accountA.S21-0042"},
		{"farm.S21-0042", "accountB.{worker}", "accountB.S21-0042"},
		{"S19-123", "accountA.{worker}", "accountA.S19-123"},
		{"farm.rack1.S19-7", "accountA.{worker}", "accountA.rack1.S19-7"},
		{"farm.S21-0042", "accountA", "accountA"},
		{"vnish.fee01", "accountA.{worker}", "accountA.fee01"},
		{"farm.", "accountA.{worker}", "accountA.default"},
		{"farm.S21", "{login}", "farm.S21"},
	}
	for _, tt := range tests {
		rw := RewriteLogin(tt.login, "asicpass", tt.tmpl, "x")
		if rw.User != tt.want {
			t.Errorf("%q with %q: got %q, want %q", tt.login, tt.tmpl, rw.User, tt.want)
		}
	}
}

func TestRewritePassword(t *testing.T) {
	if rw := RewriteLogin("farm.a", "d=1024", "acc.{worker}", "x,{password}"); rw.Password != "x,d=1024" {
		t.Fatalf("password = %q", rw.Password)
	}
	if rw := RewriteLogin("vnish.fee", "secret", "acc.{worker}", "poolpass"); rw.Password != "poolpass" {
		t.Fatalf("arbitrary login kept ASIC password, got %q", rw.Password)
	}
}

func TestReplaceParamsKeepsVersionBits(t *testing.T) {
	line := []byte(`{"params": ["farm.S21", "4f2a", "00000000", "65f1a2b3", "1a2b3c4d", "1fffe000"], "id": 42, "method": "mining.submit"}` + "\n")
	msg, err := Parse(line)
	if err != nil {
		t.Fatal(err)
	}
	params, _ := ParamsArray(msg.Params)
	orig := append([]json.RawMessage{}, params...)
	params[0] = EncodeString("accountA.S21")
	out, err := ReplaceParams(line, params)
	if err != nil {
		t.Fatal(err)
	}
	m2, _ := Parse(out)
	p2, _ := ParamsArray(m2.Params)
	if len(p2) != 6 {
		t.Fatalf("got %d params", len(p2))
	}
	for i := 1; i < 6; i++ {
		if !bytes.Equal(p2[i], orig[i]) {
			t.Errorf("param %d changed: %s -> %s", i, orig[i], p2[i])
		}
	}
	if string(m2.ID) != "42" || m2.Method != "mining.submit" {
		t.Errorf("envelope changed: %s", out)
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("missing newline")
	}
}

func TestStringIDKept(t *testing.T) {
	line := []byte(`{"id":"abc","method":"mining.authorize","params":["a","b"]}`)
	out, err := ReplaceParams(line, []json.RawMessage{EncodeString("x"), EncodeString("y")})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"id":"abc"`) {
		t.Errorf("string id lost: %s", out)
	}
}

func TestErrorReason(t *testing.T) {
	cases := map[string]string{
		`[23, "Low difficulty share", null]`: "Low difficulty share",
		`{"code": 21, "message": "Stale"}`:   "Stale",
		`"Job not found"`:                    "Job not found",
		`null`:                               "",
	}
	for in, want := range cases {
		if got := ErrorReason(json.RawMessage(in)); got != want {
			t.Errorf("ErrorReason(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestNumberAtRejectsNonFiniteStrings(t *testing.T) {
	params := []json.RawMessage{
		json.RawMessage(`"NaN"`),
		json.RawMessage(`"+Inf"`),
		json.RawMessage(`"-Inf"`),
		json.RawMessage(`"1024.5"`),
	}
	for i := 0; i < 3; i++ {
		if _, ok := NumberAt(params, i); ok {
			t.Fatalf("accepted non-finite value %s", params[i])
		}
	}
	if got, ok := NumberAt(params, 3); !ok || got != 1024.5 {
		t.Fatalf("finite numeric string: got %v ok=%v", got, ok)
	}
}

func TestLineReader(t *testing.T) {
	long := strings.Repeat("a", 10000)
	r := NewLineReader(strings.NewReader("\r\n{\"a\":1}\r\n"+long+"\n{\"b\":2}\n"), 20000)
	l1, err := r.ReadLine()
	if err != nil || string(l1) != "{\"a\":1}\r\n" {
		t.Fatalf("line1 %q %v", l1, err)
	}
	l2, err := r.ReadLine()
	if err != nil || len(l2) != 10001 {
		t.Fatalf("line2 len %d %v", len(l2), err)
	}
	l3, err := r.ReadLine()
	if err != nil || string(l3) != "{\"b\":2}\n" {
		t.Fatalf("line3 %q %v", l3, err)
	}

	r = NewLineReader(strings.NewReader(long+"\n"), 5000)
	if _, err := r.ReadLine(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("want ErrLineTooLong, got %v", err)
	}
}

func TestLineReaderErrorIsSticky(t *testing.T) {
	r := NewLineReader(strings.NewReader(strings.Repeat("a", 100)+"\n{\"id\":1}\n"), 10)
	if _, err := r.ReadLine(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("want ErrLineTooLong, got %v", err)
	}
	if line, err := r.ReadLine(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("reader resumed mid-line: %q %v", line, err)
	}
}
