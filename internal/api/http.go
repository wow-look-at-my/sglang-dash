package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/wow-look-at-my/sglang-dash/internal/diagnose"
	"github.com/wow-look-at-my/sglang-dash/internal/events"
	"github.com/wow-look-at-my/sglang-dash/internal/metrics"
	"github.com/wow-look-at-my/sglang-dash/internal/requests"
)

// Status is the header strip's payload.
type Status struct {
	Mode          string            `json:"mode"`
	Upstream      string            `json:"upstream,omitempty"`
	UptimeSeconds int64             `json:"uptimeSeconds"`
	Metrics       metrics.Snapshot  `json:"metrics"`
	Requests      requests.Totals   `json:"requests"`
	Baseline      diagnose.Baseline `json:"baseline"`
	KnownSeries   []string          `json:"knownSeries"`
}

// Register mounts the dashboard's own endpoints on the mux.
func (h *Hub) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status", h.handleStatus)
	mux.HandleFunc("GET /api/events", h.handleEvents)
	mux.HandleFunc("GET /api/requests", h.handleRequests)
	mux.HandleFunc("GET /api/requests/{id}", h.handleRequest)
	mux.HandleFunc("GET /api/cache", h.handleCache)
	mux.HandleFunc("GET /api/metrics/series", h.handleSeries)
	mux.HandleFunc("GET /api/stream", h.handleStream)
}

func (h *Hub) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.Status())
}

// Status assembles the header payload.
func (h *Hub) Status() Status {
	return Status{
		Mode:          h.Mode,
		Upstream:      h.Upstream,
		UptimeSeconds: int64(time.Since(h.StartedAt).Seconds()),
		Metrics:       h.Metrics.Last(),
		Requests:      h.Requests.Totals(),
		Baseline:      h.Diag.Baseline(),
		KnownSeries:   h.Metrics.Names(),
	}
}

func (h *Hub) handleEvents(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"events": h.Events.Recent(intParam(r, "limit", 500))})
}

func (h *Hub) handleRequests(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"requests": h.Requests.Recent(intParam(r, "limit", 300))})
}

func (h *Hub) handleRequest(w http.ResponseWriter, r *http.Request) {
	rec, ok := h.Requests.Get(r.PathValue("id"))
	if !ok {
		http.Error(w, "no request with that id is still in the ring", http.StatusNotFound)
		return
	}
	writeJSON(w, rec)
}

func (h *Hub) handleCache(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.Cache.Snapshot(intParam(r, "evictions", 100)))
}

func (h *Hub) handleSeries(w http.ResponseWriter, r *http.Request) {
	key := r.URL.Query().Get("key")
	if key == "" {
		http.Error(w, "pass ?key=<series>, one of the names in /api/status knownSeries", http.StatusBadRequest)
		return
	}
	points, ok := h.Metrics.History(key)
	if !ok {
		http.Error(w, "that series has never been scraped from this upstream", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"key": key, "points": points})
}

// streamPayload is what the live feed sends per frame.
type streamPayload struct {
	Type   string        `json:"type"`
	Status *Status       `json:"status,omitempty"`
	Event  *events.Event `json:"event,omitempty"`
}

// handleStream pushes status frames on a timer and every event as it happens.
func (h *Hub) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "this server cannot stream to that client", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch, stop := h.Events.Subscribe(256)
	defer stop()

	send := func(p streamPayload) bool {
		body, err := json.Marshal(p)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	status := h.Status()
	if !send(streamPayload{Type: "status", Status: &status}) {
		return
	}

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			e := ev
			if !send(streamPayload{Type: "event", Event: &e}) {
				return
			}
		case <-ticker.C:
			s := h.Status()
			if !send(streamPayload{Type: "status", Status: &s}) {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	if err := enc.Encode(v); err != nil {
		// The status line is already written, so this is the last honest place.
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func intParam(r *http.Request, name string, def int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return n
}
