package pool

import (
	"bufio"
	"fmt"
	"net"
	"testing"
)

func callWithResult(t *testing.T, result string) error {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err == nil {
			_, _ = fmt.Fprintf(server, `{"id":1,"result":%s,"error":null}`+"\n", result)
		}
	}()
	return call(client, bufio.NewReader(client), 1, "mining.subscribe", []any{"test"}, false)
}

func TestSubscribeCheckRequiresValidResult(t *testing.T) {
	for _, result := range []string{"false", `{}`, `[]`, `[[],"extra"]`} {
		if err := callWithResult(t, result); err == nil {
			t.Errorf("result %s was accepted", result)
		}
	}
	if err := callWithResult(t, `[[["mining.notify","id"]],"extranonce",4]`); err != nil {
		t.Fatalf("valid subscribe result rejected: %v", err)
	}
}

func TestCallAcceptsStringEchoedID(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		r := bufio.NewReader(server)
		if _, err := r.ReadBytes('\n'); err == nil {
			_, _ = fmt.Fprint(server, `{"id":"1","result":true,"error":null}`+"\n")
		}
	}()
	if err := call(client, bufio.NewReader(client), 1, "mining.authorize", []any{"u", "x"}, true); err != nil {
		t.Fatalf("string id not matched: %v", err)
	}
}
