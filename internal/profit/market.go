// Package profit compares the SHA-256 coins of the configured pools by
// expected revenue and can switch the active pool to the most profitable
// one.
package profit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/stats"
)

// staleAfter is how old the data of a coin may be before it is not used
// for a decision.
const staleAfter = 6 * time.Hour

// Coin is the market data of one coin: 24-hour averages when the source has
// them, so a single block does not decide a switch.
type Coin struct {
	Tag        string  `json:"tag"`
	Name       string  `json:"name"`
	Difficulty float64 `json:"difficulty"`
	// DifficultyNow is the latest network difficulty, for the solo odds; the
	// decisions use the 24-hour Difficulty.
	DifficultyNow float64   `json:"difficulty_now"`
	BlockReward   float64   `json:"block_reward"` // coins per block, fees included
	PriceBTC      float64   `json:"price_btc"`
	Updated       time.Time `json:"updated"`
	Stale         bool      `json:"stale"` // too old or flagged by the source: not used for decisions
}

// RevenueBTC is the expected revenue of 1 TH/s over a day, in BTC.
func (c Coin) RevenueBTC() float64 {
	if c.Difficulty <= 0 {
		return 0
	}
	return 1e12 * 86400 / (c.Difficulty * stats.HashesPerDifficulty) * c.BlockReward * c.PriceBTC
}

// Market is one snapshot of the market.
type Market struct {
	Coins   map[string]Coin // by upper-case tag
	BTCUSD  float64         // 0 when unknown
	Fetched time.Time
}

// Fetcher loads the market.
type Fetcher func(ctx context.Context) (*Market, error)

// WhatToMineURL is the public ASIC list of WhatToMine: every SHA-256 coin
// with difficulty, block reward and price, no key needed.
const WhatToMineURL = "https://whattomine.com/asic.json"

type wtmCoin struct {
	Tag            string  `json:"tag"`
	Algorithm      string  `json:"algorithm"`
	BlockReward    float64 `json:"block_reward"`
	BlockReward24  float64 `json:"block_reward24"`
	Difficulty     float64 `json:"difficulty"`
	Difficulty24   float64 `json:"difficulty24"`
	ExchangeRate   float64 `json:"exchange_rate"`
	ExchangeRate24 float64 `json:"exchange_rate24"`
	Lagging        bool    `json:"lagging"`
	Timestamp      int64   `json:"timestamp"`
}

// WhatToMine fetches url (WhatToMineURL) with client.
func WhatToMine(client *http.Client, url string) Fetcher {
	return func(ctx context.Context) (*Market, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "SimpleMiningProxy (profit switching, one request per check)")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
		}
		return parseWhatToMine(io.LimitReader(res.Body, 16<<20), time.Now())
	}
}

func parseWhatToMine(r io.Reader, now time.Time) (*Market, error) {
	var body struct {
		Coins map[string]wtmCoin `json:"coins"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return nil, fmt.Errorf("market data: %w", err)
	}
	m := &Market{Coins: map[string]Coin{}, Fetched: now}
	avg := func(day, cur float64) float64 {
		if day > 0 {
			return day
		}
		return cur
	}
	for name, w := range body.Coins {
		tag := strings.ToUpper(strings.TrimSpace(w.Tag))
		if w.Algorithm != "SHA-256" || tag == "" || tag == "NICEHASH" {
			continue
		}
		c := Coin{
			Tag: tag, Name: name,
			Difficulty:    avg(w.Difficulty24, w.Difficulty),
			DifficultyNow: avg(w.Difficulty, w.Difficulty24),
			BlockReward:   avg(w.BlockReward24, w.BlockReward),
			PriceBTC:      avg(w.ExchangeRate24, w.ExchangeRate),
			Updated:       time.Unix(w.Timestamp, 0).UTC(),
		}
		if tag == "BTC" {
			// WhatToMine gives the BTC entry its USD price in exchange_rate.
			if c.PriceBTC > 100 && c.PriceBTC < 1e8 {
				m.BTCUSD = c.PriceBTC
			}
			c.PriceBTC = 1
		}
		c.Stale = w.Lagging || now.Sub(c.Updated) > staleAfter || c.Difficulty <= 0 || c.BlockReward <= 0 || c.PriceBTC <= 0
		if old, dup := m.Coins[tag]; dup && !old.Stale {
			continue
		}
		m.Coins[tag] = c
	}
	if len(m.Coins) == 0 {
		return nil, errors.New("market data: no SHA-256 coins")
	}
	return m, nil
}
