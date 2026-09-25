package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHistoryAPI(t *testing.T) {
	e := newEnv(t)
	to := time.Now().Unix() / 60 * 60 // whole minutes: exactly 60 steps
	w := e.do("GET", fmt.Sprintf("/api/history?from=%d&to=%d&points=60", to-3600, to), "", bearer)
	var body struct {
		Series struct {
			Step     int64      `json:"step"`
			Time     []int64    `json:"time"`
			Hashrate []*float64 `json:"hashrate"`
		} `json:"series"`
		Retention string `json:"retention"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if body.Series.Step != 60 || len(body.Series.Time) != 60 || body.Series.Hashrate[0] != nil || body.Retention != "8760h" {
		t.Fatalf("empty history: %s", w.Body)
	}
	for _, q := range []string{"from=10&to=5", "from=abc", "from=0&to=999999999999"} {
		if w := e.do("GET", "/api/history?"+q, "", bearer); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", q, w.Code, w.Body)
		}
	}

	w = e.do("GET", "/api/history/worker?name=farm.a", "", bearer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"online":[`) {
		t.Fatalf("worker history: %d %s", w.Code, w.Body)
	}
	if w := e.do("GET", "/api/history/worker", "", bearer); w.Code != http.StatusBadRequest {
		t.Fatalf("worker without a name: %d", w.Code)
	}
	if w := e.do("GET", "/api/history/workers", "", bearer); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"workers":[]`) {
		t.Fatalf("workers: %d %s", w.Code, w.Body)
	}
}

func TestTimedAPI(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/timed", "", bearer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"off","period":"30m","duration":"10m","target":""`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	// The mark moves to the last pool that got it.
	for _, name := range []string{"Pool A", "Solo"} {
		body := `{"name":"` + name + `","host":"x.example.com","port":3333,"username":"u","timed_target":true}`
		if w := e.do("POST", "/api/pools", body, bearer); w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body)
		}
	}
	w = e.do("GET", "/api/pools", "", bearer)
	var body struct {
		Pools []struct {
			ID          string `json:"id"`
			TimedTarget bool   `json:"timed_target"`
		} `json:"pools"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("pools: %s", w.Body)
	}
	if p := body.Pools; len(p) != 2 || p[0].TimedTarget || !p[1].TimedTarget {
		t.Fatalf("timed_target: %+v", p)
	}
	if w := e.do("GET", "/api/timed", "", bearer); !strings.Contains(w.Body.String(), `"target":"solo"`) {
		t.Fatalf("status with a target: %s", w.Body)
	}
}

func TestProfitAPI(t *testing.T) {
	e := newEnv(t)
	w := e.do("GET", "/api/profit", "", bearer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"mode":"off"`) || !strings.Contains(w.Body.String(), `"report":null`) {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	w = e.do("POST", "/api/profit/check", `{}`, bearer)
	var body struct {
		Report struct {
			Scheduled bool   `json:"scheduled"`
			Decision  string `json:"decision"`
			Coins     []struct {
				Tag        string  `json:"tag"`
				RevenueUSD float64 `json:"revenue_usd"`
			} `json:"coins"`
		} `json:"report"`
		LastRun *string `json:"last_run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || w.Code != http.StatusOK {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	// No pools: nothing to decide, but the coins are shown; a manual check
	// does not start the schedule.
	if body.Report.Scheduled || body.Report.Decision != "no_candidates" || len(body.Report.Coins) != 1 ||
		body.Report.Coins[0].RevenueUSD <= 0 || body.LastRun != nil {
		t.Fatalf("check: %s", w.Body)
	}
}
