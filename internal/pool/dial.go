package pool

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stratum"
)

const (
	keepAlive    = 30 * time.Second
	checkTimeout = 10 * time.Second
	userAgent    = "sha256-stratum-proxy/1.0"
)

// Dial connects to one address of a pool over TCP or TLS within timeout and
// returns the TCP connect time (one round trip; DNS and the TLS handshake are
// not included). DNS is resolved on every call. For TLS the certificate is
// verified against the system CAs with SNI = host unless tls_skip_verify.
func Dial(ctx context.Context, p state.Pool, a state.Address, timeout time.Duration) (net.Conn, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var mu sync.Mutex
	var start time.Time
	nd := &net.Dialer{
		KeepAlive: keepAlive,
		// Called right before connect(2), after DNS: the start of the ping.
		ControlContext: func(context.Context, string, string, syscall.RawConn) error {
			mu.Lock()
			start = time.Now()
			mu.Unlock()
			return nil
		},
	}
	conn, err := nd.DialContext(ctx, "tcp", a.String())
	if err != nil {
		return nil, 0, err
	}
	mu.Lock()
	rtt := time.Since(start)
	mu.Unlock()
	if rtt <= 0 { // coarse clocks (Windows) can report 0 for a local connect
		rtt = time.Microsecond
	}
	if !p.TLS {
		return conn, rtt, nil
	}
	tc := tls.Client(conn, &tls.Config{
		ServerName:         a.Host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: p.TLSSkipVerify,
	})
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, 0, fmt.Errorf("tls: %w", err)
	}
	return tc, rtt, nil
}

// Check performs a Stratum-level check of one address: connect,
// mining.subscribe and, if authorize is set, mining.authorize for the test
// worker. The background probe does not authorize to avoid
// creating workers on the pool. rtt is the connect time, also on failure
// after a successful connect.
func Check(ctx context.Context, p state.Pool, a state.Address, authorize bool, testWorker string) (rtt time.Duration, err error) {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	conn, rtt, err := Dial(ctx, p, a, checkTimeout)
	if err != nil {
		return 0, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()

	r := bufio.NewReaderSize(conn, 4096)
	if err := call(conn, r, 1, "mining.subscribe", []any{userAgent}, false); err != nil {
		return rtt, fmt.Errorf("mining.subscribe: %w", timedOut(ctx, err))
	}
	if !authorize {
		return rtt, nil
	}
	rw := stratum.RewriteLogin(testWorker, "x", p.Username, p.Password)
	if err := call(conn, r, 2, "mining.authorize", []any{rw.User, rw.Password}, true); err != nil {
		return rtt, fmt.Errorf("mining.authorize as %q: %w", rw.User, timedOut(ctx, err))
	}
	return rtt, nil
}

// errNoAnswer replaces the low-level error of a read cut short by the check
// deadline ("i/o timeout", or "use of closed network connection" when the
// deadline closed the socket under the reader).
var errNoAnswer = fmt.Errorf("no answer within %s", checkTimeout)

func timedOut(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, os.ErrDeadlineExceeded) {
		return errNoAnswer
	}
	return err
}

// AddrResult is the manual test of one address.
type AddrResult struct {
	Address state.Address
	RTT     time.Duration
	Err     error
}

// CheckAll tests every address of a pool in parallel.
func CheckAll(ctx context.Context, p state.Pool, authorize bool, testWorker string) []AddrResult {
	out := make([]AddrResult, len(p.Addresses))
	var wg sync.WaitGroup
	for i, a := range p.Addresses {
		wg.Add(1)
		go func(i int, a state.Address) {
			defer wg.Done()
			rtt, err := Check(ctx, p, a, authorize, testWorker)
			out[i] = AddrResult{Address: a, RTT: rtt, Err: err}
		}(i, a)
	}
	wg.Wait()
	return out
}

// call sends a request and waits for the response with the same id,
// skipping notifications (set_difficulty, notify) that may come first.
func call(conn net.Conn, r *bufio.Reader, id int, method string, params []any, wantTrue bool) error {
	req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	lr := stratum.NewLineReader(r, 64*1024)
	for {
		line, err := lr.ReadLine()
		if err != nil {
			return err
		}
		msg, err := stratum.Parse(line)
		if err != nil {
			return fmt.Errorf("invalid response: %w", err)
		}
		// Some pools echo a numeric id back as a string; accept both forms.
		if want := strconv.Itoa(id); msg.Method != "" || (msg.IDKey() != want && msg.IDKey() != strconv.Quote(want)) {
			continue
		}
		if !stratum.IsNull(msg.Error) {
			return errors.New("pool error: " + stratum.ErrorReason(msg.Error))
		}
		if stratum.IsNull(msg.Result) {
			return errors.New("empty result")
		}
		if wantTrue {
			if !stratum.ResultTrue(msg.Result) {
				return errors.New("rejected by pool (result " + string(msg.Result) + ")")
			}
		} else {
			var subscription []json.RawMessage
			if err := json.Unmarshal(msg.Result, &subscription); err != nil || len(subscription) < 3 {
				return errors.New("invalid mining.subscribe result " + string(msg.Result))
			}
		}
		return nil
	}
}
