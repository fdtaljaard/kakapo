package api

import (
	"log"
	"net/http"
	"sync"
	"time"
)

// Presence counts the UI windows connected to /api/events. The app uses it to stop when
// the last window has gone, instead of guessing from the browser's process. A long fix running in
// the background (docs/phase4f-contract.md) counts as a window: the service raises and lowers the
// busy count (Busy, Idle), so the app stays up until the run ends and then follows the normal rule.
type Presence struct {
	mu        sync.Mutex
	open      int
	busy      int       // long fixes running in the background
	seen      bool      // a window has connected at least once
	zeroSince time.Time // when open and busy last dropped to zero together
}

// Busy counts a long fix that started running in the background as a window.
func (p *Presence) Busy() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy++
}

// Idle counts a long fix that ended; with no window open, the grace starts now.
func (p *Presence) Idle() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.busy--
	if p.open+p.busy == 0 {
		p.zeroSince = time.Now()
	}
}

func (p *Presence) add() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open++
	if !p.seen {
		log.Print("window connected")
	}
	p.seen = true
}

func (p *Presence) remove() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.open--
	if p.open+p.busy == 0 {
		p.zeroSince = time.Now()
	}
}

// Gone reports true when a window has connected before and none has been connected, and no long
// fix has been running, for longer than grace. A page reload or a slow reconnect is well inside the
// grace.
func (p *Presence) Gone(grace time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.seen && p.open == 0 && p.busy == 0 && time.Since(p.zeroSince) > grace
}

// WithEvents adds GET /api/events, a server-sent-events stream the page keeps open for as
// long as it is shown. Today it only carries keep-alive pings; later the live sample rows.
func WithEvents(next http.Handler, p *Presence) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" || r.Method != http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(": connected\n\n"))
		flusher.Flush()

		p.add()
		defer p.remove()

		ping := time.NewTicker(15 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ping.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}
