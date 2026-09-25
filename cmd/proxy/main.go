// Command proxy is the SHA256 Stratum V1 proxy with an embedded admin UI.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/famfamfam/simple-mining-proxy/internal/admin"
	"github.com/famfamfam/simple-mining-proxy/internal/config"
	"github.com/famfamfam/simple-mining-proxy/internal/events"
	"github.com/famfamfam/simple-mining-proxy/internal/history"
	"github.com/famfamfam/simple-mining-proxy/internal/listener"
	"github.com/famfamfam/simple-mining-proxy/internal/pool"
	"github.com/famfamfam/simple-mining-proxy/internal/profit"
	"github.com/famfamfam/simple-mining-proxy/internal/session"
	"github.com/famfamfam/simple-mining-proxy/internal/settings"
	"github.com/famfamfam/simple-mining-proxy/internal/state"
	"github.com/famfamfam/simple-mining-proxy/internal/stats"
	"github.com/famfamfam/simple-mining-proxy/internal/timed"
	"github.com/famfamfam/simple-mining-proxy/internal/tlsutil"
)

const shutdownTimeout = 10 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// healthcheck is the Docker healthcheck: distroless has no curl.
func healthcheck() int {
	c := http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get(config.AdminHealthURL())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.Status)
		return 1
	}
	return 0
}

func parseLevel(s string) slog.Level {
	var l slog.Level
	if err := l.UnmarshalText([]byte(s)); err != nil {
		return slog.LevelInfo
	}
	return l
}

func run() error {
	started := time.Now()
	level := new(slog.LevelVar)
	opts := &slog.HandlerOptions{Level: level}
	format := strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT")))
	if format == "text" {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, opts)))
	} else {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, opts)))
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	st, err := state.Open(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		return err
	}
	set, unknown, err := settings.NewStore(st.Current().Settings)
	if err != nil {
		return err
	}
	for _, k := range unknown {
		slog.Warn("unknown setting in state.json is ignored", "key", k)
	}
	level.Set(parseLevel(set.Get().LogLevel))
	set.OnChange(func(v *settings.Values) { level.Set(parseLevel(v.LogLevel)) })

	ev := events.New(500)
	reg := session.NewRegistry(ev)
	set.OnChange(reg.RefreshIdleDeadlines)
	collector := stats.NewCollector()
	mgr := pool.NewManager(st, set, reg, ev)

	srv := &session.Server{Settings: set, Router: mgr, Registry: reg, Events: ev, Stats: collector}

	hist, err := history.New(filepath.Join(cfg.DataDir, "history"),
		func() history.Sample {
			return history.Sample{Totals: collector.Totals(), Miners: reg.Counts().ByPool, Online: reg.Workers()}
		},
		func() history.Retention {
			v := set.Get()
			return history.Retention{Detail: v.HistoryDetail, Total: v.HistoryRetention, Miners: v.HistoryMiners}
		})
	if err != nil {
		return fmt.Errorf("history: %w", err)
	}
	set.OnChange(func(*settings.Values) { go hist.Prune() })

	timedSw := timed.New(timed.Deps{Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(cfg.DataDir, "timed.json")})
	profitSw := profit.New(profit.Deps{
		Settings: set, Pools: mgr, Events: ev, Path: filepath.Join(cfg.DataDir, "profit.json"),
		Fetch: profit.WhatToMine(&http.Client{}, profit.WhatToMineURL),
		Home:  timedSw.Home,
	})

	var certs *tlsutil.Certs
	if cfg.StratumTLSAddr != "" {
		var generated bool
		certs, generated, err = tlsutil.Load(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSSelfSignedHosts)
		if err != nil {
			return err
		}
		info := certs.Info()
		if generated {
			ev.Warn("tls_self_signed", "generated a self-signed TLS certificate for %v, SHA-256 %s", info.DNSNames, info.SHA256)
		}
		base := &tls.Config{GetCertificate: certs.GetCertificate}
		// A fresh config per connection picks up tls_min_version without a restart.
		srv.TLSConfig = func() *tls.Config {
			c := base.Clone()
			c.MinVersion = tlsutil.MinVersion(set.Get().TLSMinVersion)
			return c
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	limits := listener.NewLimits(set, ev)
	var listeners []*listener.Listener
	if cfg.StratumTCPAddr != "" {
		listeners = append(listeners, &listener.Listener{Name: "tcp", Addr: cfg.StratumTCPAddr, Server: srv, Limits: limits})
	}
	if cfg.StratumTLSAddr != "" {
		listeners = append(listeners, &listener.Listener{Name: "tls", Addr: cfg.StratumTLSAddr, TLS: true, Server: srv, Limits: limits})
	}
	for _, l := range listeners {
		if err := l.Listen(ctx); err != nil {
			return err
		}
	}

	adminLn, err := net.Listen("tcp", cfg.AdminAddr)
	if err != nil {
		return fmt.Errorf("admin listener on %s: %w", cfg.AdminAddr, err)
	}
	httpSrv := &http.Server{
		Handler: admin.New(admin.Deps{
			Config: cfg, State: st, Settings: set, Pools: mgr, Registry: reg,
			Stats: collector, History: hist, Profit: profitSw, Timed: timedSw, Events: ev, Certs: certs, Started: started,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	slog.Info("starting", "config", cfg)
	snap := mgr.Snapshot()
	if len(snap.Pools) == 0 {
		ev.Warn("no_pools", "no pools configured yet: add one in the admin UI")
	}
	ev.Info("started", "proxy started: %d pools, active %q", len(snap.Pools), snap.Active)

	var sessions sync.WaitGroup
	var acceptors sync.WaitGroup
	for _, l := range listeners {
		acceptors.Add(1)
		go func(l *listener.Listener) {
			defer acceptors.Done()
			l.Serve(ctx, &sessions)
		}(l)
	}
	go limits.ReportRejected(ctx)
	go mgr.Run(ctx)
	histDone := make(chan struct{})
	go func() { hist.Run(ctx); close(histDone) }()
	go profitSw.Run(ctx)
	go timedSw.Run(ctx)
	httpErr := make(chan error, 1)
	go func() {
		slog.Info("admin UI listening", "addr", adminLn.Addr().String())
		httpErr <- httpSrv.Serve(adminLn)
	}()

	select {
	case <-ctx.Done():
	case err := <-httpErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("admin server: %w", err)
		}
	}

	// Shutdown: stop accepting and wait until accept loops can no longer add
	// sessions, then close all sessions and stop HTTP.
	slog.Info("shutting down")
	stop()
	acceptors.Wait()
	reg.CloseAll("shutdown")
	shCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	httpSrv.Shutdown(shCtx)
	done := make(chan struct{})
	go func() { sessions.Wait(); close(done) }()
	select {
	case <-done:
	case <-shCtx.Done():
		slog.Warn("some sessions did not finish in time")
	}
	// The history writes the unfinished minute when ctx is done.
	select {
	case <-histDone:
	case <-shCtx.Done():
	}
	return nil
}
