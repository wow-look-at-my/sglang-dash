// Package config holds the dashboard's startup options.
package config

import (
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config is the resolved startup configuration.
type Config struct {
	Listen        string
	Upstream      *url.URL
	MetricsURL    *url.URL
	ScrapeEvery   time.Duration
	Demo          bool
	RedactPrompts bool
	MaxCacheNodes int
	MaxRequests   int
	MaxEvents     int
	SlowFloorMs   int64
	SlowFactor    float64
}

// Parse reads flags and the environment. Every flag has an SGLANG_DASH_
// environment twin so a container needs no command line.
func Parse(args []string) (Config, error) {
	fs := flag.NewFlagSet("sglang-dash", flag.ContinueOnError)
	var (
		listen      = fs.String("listen", env("LISTEN", ":8080"), "address to serve the dashboard and the proxy on")
		upstream    = fs.String("upstream", env("UPSTREAM", ""), "base URL of the SGLang server to proxy and observe")
		metrics     = fs.String("metrics-url", env("METRICS_URL", ""), "Prometheus endpoint to scrape (default: <upstream>/metrics)")
		scrape      = fs.Duration("scrape-interval", envDuration("SCRAPE_INTERVAL", time.Second), "how often to scrape upstream metrics")
		demo        = fs.Bool("demo", envBool("DEMO", false), "run against a built-in traffic simulator instead of a real server")
		redact      = fs.Bool("redact-prompts", envBool("REDACT_PROMPTS", false), "store prompt hashes and lengths only, never prompt text")
		maxNodes    = fs.Int("max-cache-nodes", envInt("MAX_CACHE_NODES", 4000), "prefix-tree nodes to keep before the coldest are forgotten")
		maxRequests = fs.Int("max-requests", envInt("MAX_REQUESTS", 2000), "requests to keep in the ring")
		maxEvents   = fs.Int("max-events", envInt("MAX_EVENTS", 5000), "events to keep in the ring")
		slowFloor   = fs.Int64("slow-floor-ms", int64(envInt("SLOW_FLOOR_MS", 2000)), "wall time under which a request is never called slow")
		slowFactor  = fs.Float64("slow-factor", envFloat("SLOW_FACTOR", 2.5), "multiple of the rolling median wall time that marks a request slow")
	)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "sglang-dash: an observability dashboard that sits in front of an SGLang server.\n\n"+
			"It proxies /v1 traffic to the server, records every request, scrapes the server's\n"+
			"Prometheus metrics, and reconstructs the prefix cache from what it sees.\n\n"+
			"Usage:\n  sglang-dash -upstream http://127.0.0.1:30000\n  sglang-dash -demo\n\nFlags:\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg := Config{
		Listen: *listen, ScrapeEvery: *scrape, Demo: *demo, RedactPrompts: *redact,
		MaxCacheNodes: *maxNodes, MaxRequests: *maxRequests, MaxEvents: *maxEvents,
		SlowFloorMs: *slowFloor, SlowFactor: *slowFactor,
	}

	// An unconfigured dashboard refuses to start rather than serving a page of
	// empty panels that looks like a healthy, idle server.
	if *upstream == "" && !*demo {
		return Config{}, errors.New("no server to observe: pass -upstream http://host:port (or -demo to run the built-in simulator)")
	}
	if *upstream != "" && *demo {
		return Config{}, errors.New("-upstream and -demo are mutually exclusive: one proxies a real server, the other fabricates traffic")
	}

	if *upstream != "" {
		u, err := parseBase(*upstream)
		if err != nil {
			return Config{}, fmt.Errorf("-upstream: %w", err)
		}
		cfg.Upstream = u
		m := *metrics
		if m == "" {
			m = strings.TrimSuffix(u.String(), "/") + "/metrics"
		}
		mu, err := url.Parse(m)
		if err != nil {
			return Config{}, fmt.Errorf("-metrics-url: %w", err)
		}
		cfg.MetricsURL = mu
	}
	if cfg.ScrapeEvery < 100*time.Millisecond {
		return Config{}, errors.New("-scrape-interval must be at least 100ms")
	}
	return cfg, nil
}

func parseBase(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("%q has no host", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	return u, nil
}

func env(name, def string) string {
	if v, ok := os.LookupEnv("SGLANG_DASH_" + name); ok {
		return v
	}
	return def
}

// envBool and its siblings refuse a value they cannot read rather than falling
// back to the default: a typo in a container's environment must not silently
// turn a setting off.
func envBool(name string, def bool) bool {
	v, ok := os.LookupEnv("SGLANG_DASH_" + name)
	if !ok {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	}
	fatalf("SGLANG_DASH_%s=%q is not a boolean", name, v)
	return def
}

func envInt(name string, def int) int {
	v, ok := os.LookupEnv("SGLANG_DASH_" + name)
	if !ok {
		return def
	}
	var out int
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%d", &out); err != nil {
		fatalf("SGLANG_DASH_%s=%q is not an integer", name, v)
	}
	return out
}

func envFloat(name string, def float64) float64 {
	v, ok := os.LookupEnv("SGLANG_DASH_" + name)
	if !ok {
		return def
	}
	var out float64
	if _, err := fmt.Sscanf(strings.TrimSpace(v), "%g", &out); err != nil {
		fatalf("SGLANG_DASH_%s=%q is not a number", name, v)
	}
	return out
}

func envDuration(name string, def time.Duration) time.Duration {
	v, ok := os.LookupEnv("SGLANG_DASH_" + name)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		fatalf("SGLANG_DASH_%s=%q is not a duration", name, v)
	}
	return d
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "sglang-dash: "+format+"\n", args...)
	os.Exit(2)
}
