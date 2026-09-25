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
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/rtt"
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
	// Efficiency is the share of the block rate that the difficulty alone
	// suggests which miners really get: below 1 on eCash, where the Real Time
	// Target makes the first minutes after a block practically dead.
	Efficiency float64 `json:"efficiency"`
}

// efficiency of the coin with tag, see Coin.Efficiency.
func efficiency(tag string) float64 {
	if tag == "XEC" {
		return rtt.Efficiency
	}
	return 1
}

// RevenueBTC is the expected revenue of 1 TH/s over a day, in BTC.
func (c Coin) RevenueBTC() float64 {
	if c.Difficulty <= 0 {
		return 0
	}
	eff := c.Efficiency
	if eff <= 0 {
		eff = 1
	}
	return 1e12 * 86400 / (c.Difficulty * stats.HashesPerDifficulty) * c.BlockReward * c.PriceBTC * eff
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
		body, err := get(ctx, client, url)
		if err != nil {
			return nil, err
		}
		defer body.Close()
		return parseWhatToMine(body, time.Now())
	}
}

// get opens url for reading; the caller closes the body.
func get(ctx context.Context, client *http.Client, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "SimpleMiningProxy (market data, one request per check)")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK {
		res.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, res.StatusCode)
	}
	return struct {
		io.Reader
		io.Closer
	}{io.LimitReader(res.Body, 16<<20), res.Body}, nil
}

// WhatsOnChainURL is the public BSV API: WhatToMine does not list BSV.
const WhatsOnChainURL = "https://api.whatsonchain.com/v1/bsv/main"

// WithBSV adds BSV from WhatsOnChain (url) to the market that base fetches.
// BSV is optional: when WhatsOnChain fails, the market comes without it.
func WithBSV(client *http.Client, url string, base Fetcher) Fetcher {
	return func(ctx context.Context) (*Market, error) {
		m, err := base(ctx)
		if err != nil {
			return nil, err
		}
		if c, err := fetchBSV(ctx, client, url, m.BTCUSD, time.Now()); err == nil {
			m.Coins[c.Tag] = c
		} else {
			slog.Debug("market data: no BSV", "err", err)
		}
		return m, nil
	}
}

func fetchBSV(ctx context.Context, client *http.Client, url string, btcUSD float64, now time.Time) (Coin, error) {
	var info struct {
		Blocks     int64   `json:"blocks"`
		Difficulty float64 `json:"difficulty"`
	}
	var rate struct {
		Rate float64 `json:"rate"`
	}
	for path, v := range map[string]any{"/chain/info": &info, "/exchangerate": &rate} {
		body, err := get(ctx, client, url+path)
		if err != nil {
			return Coin{}, err
		}
		err = json.NewDecoder(body).Decode(v)
		body.Close()
		if err != nil {
			return Coin{}, fmt.Errorf("%s: %w", path, err)
		}
	}
	return bsvCoin(info.Blocks, info.Difficulty, rate.Rate, btcUSD, now)
}

// bsvCoin builds BSV from the chain height, difficulty and USD price. The
// block reward is the subsidy of the next block; BSV fees are negligible.
// There is no 24-hour average: the current difficulty stands for both.
func bsvCoin(height int64, difficulty, priceUSD, btcUSD float64, now time.Time) (Coin, error) {
	if difficulty <= 0 || priceUSD <= 0 || btcUSD <= 0 || height <= 0 {
		return Coin{}, errors.New("BSV: incomplete data")
	}
	halvings := (height + 1) / 210000
	return Coin{
		Tag: "BSV", Name: "Bitcoin SV", Difficulty: difficulty, DifficultyNow: difficulty,
		BlockReward: 50 / math.Pow(2, float64(halvings)), PriceBTC: priceUSD / btcUSD,
		Updated: now, Efficiency: 1,
	}, nil
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
			Efficiency:    efficiency(tag),
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
