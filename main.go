// Command sglang-dash serves an observability dashboard for an SGLang server.
//
// It is a single binary: the UI is compiled into it by go:embed, so deploying
// it is copying a file.
package main

import (
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/api"
	"github.com/wow-look-at-my/sglang-dash/internal/cache"
	"github.com/wow-look-at-my/sglang-dash/internal/config"
	"github.com/wow-look-at-my/sglang-dash/internal/demo"
	"github.com/wow-look-at-my/sglang-dash/internal/diagnose"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"
	"github.com/wow-look-at-my/sglang-dash/internal/upstream"
)

// web/dist is built by `npm run build` in web/ and committed, so `go build`
// alone produces a working binary. CI rebuilds it and fails on any difference,
// so a stale asset cannot ship unnoticed.
//
//go:embed all:web/dist
var assets embed.FS

func main() {
	if err := run(); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "sglang-dash: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		return err
	}

	ui, err := fs.Sub(assets, "web/dist")
	if err != nil {
		return fmt.Errorf("the embedded UI is unreadable, which means the binary was built without web/dist: %w", err)
	}
	if _, err := fs.Stat(ui, "index.html"); err != nil {
		return errors.New("the embedded UI has no index.html: run `npm --prefix web ci && npm --prefix web run build` before `go build`")
	}

	bus := events.NewBus(cfg.MaxEvents)
	store := metrics.NewStore(900)
	tree := cache.New(cfg.MaxCacheNodes, cfg.RedactPrompts)
	reqs := requests.NewStore(cfg.MaxRequests)
	diag := diagnose.New(cfg.SlowFloorMs, cfg.SlowFactor)

	mode, upstreamName := "proxy", ""
	if cfg.Demo {
		mode = "demo"
	} else {
		upstreamName = cfg.Upstream.String()
	}
	hub := api.NewHub(bus, store, tree, reqs, diag, mode, upstreamName)

	mux := http.NewServeMux()
	hub.Register(mux)
	mux.Handle("/", http.FileServerFS(ui))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.Demo {
		// The simulator fills the same metric store a scrape would, so the
		// eviction detector classifies against real numbers here too.
		tree.SetSignals(func() cache.Signals { return api.SignalsFrom(store) })
		runner := demo.New(hub, time.Now().UnixNano())
		go runner.Run(ctx)
		bus.Publish(events.KindDashboardNotice,
			"running in demo mode: every request, metric and eviction on this screen is fabricated by the built-in simulator", nil)
		log.Printf("demo mode: no server is being observed")
	} else {
		scraper := upstream.NewScraper(cfg.MetricsURL, cfg.ScrapeEvery, store, bus)
		tree.SetSignals(scraper.Signals)
		go scraper.Run(ctx)

		proxy := upstream.NewProxy(cfg.Upstream, hub, cfg.RedactPrompts, 8<<20)
		// Everything the dashboard does not claim is the server's. Mounting it
		// this way means a client only has to change its base URL, and any
		// endpoint SGLang adds later is proxied without a code change here.
		for _, prefix := range []string{"/v1/", "/generate", "/health", "/get_model_info", "/flush_cache", "/encode", "/classify"} {
			mux.Handle(prefix, proxy)
		}
		log.Printf("proxying %s and scraping %s every %s", cfg.Upstream, cfg.MetricsURL, cfg.ScrapeEvery)
	}

	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: mux,
		// A generation can legitimately stream for a long time, so there is no
		// write deadline; the read header deadline still fences a stuck client.
		ReadHeaderTimeout: 15 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()

	log.Printf("sglang-dash listening on %s", cfg.Listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
