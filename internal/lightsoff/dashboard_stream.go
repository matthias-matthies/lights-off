package lightsoff

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// dashboardHeartbeat keeps an idle Server-Sent Events connection open through
// proxies and lets the page notice that the Dashboard has gone away.
const dashboardHeartbeat = 15 * time.Second

// eventHub is the Dashboard's subscription to the Reporter. It keeps only the
// latest event of each ticket, because that is all a page needs to draw the
// Ticket Attempt: every ticket event carries the Attempt's current phase,
// Iteration, Verdict, pull request and sandbox. It keeps a final summary or
// session-ended event apart, so a reconnect still ends on it.
//
// A subscriber is added under the same lock as the snapshot, so an event can
// neither slip between the snapshot and the live stream nor be missed by a page
// that has just reconnected.
type eventHub struct {
	mu          sync.Mutex
	latest      map[int]Event
	final       *Event // the summary or session-ended event that ends the stream
	subscribers map[chan Event]struct{}
	closed      bool
}

func newEventHub() *eventHub {
	return &eventHub{
		latest:      map[int]Event{},
		subscribers: map[chan Event]struct{}{},
	}
}

// publish records an event and hands it to every live stream. A subscriber that
// cannot keep up is disconnected rather than allowed to block the Reporter; its
// EventSource reconnects and receives the current snapshot.
func (h *eventHub) publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if endsStream(e.Kind) {
		// The final event is kept apart from the per-ticket snapshots, so that a
		// page reconnecting after it still receives it last.
		h.final = &e
	} else {
		h.latest[e.Ticket] = e
	}
	if h.closed {
		return
	}
	for subscriber := range h.subscribers {
		select {
		case subscriber <- e:
		default:
			delete(h.subscribers, subscriber)
			close(subscriber)
		}
	}
}

// endsStream reports whether e is the last event of a run or session.
func endsStream(kind EventKind) bool {
	return kind == EventSummary || kind == EventSessionEnded
}

// subscribe returns the latest event of every ticket, then a channel of new
// events. Calling cancel closes the channel and removes the subscriber.
func (h *eventHub) subscribe() ([]Event, <-chan Event, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	numbers := make([]int, 0, len(h.latest))
	for number := range h.latest {
		numbers = append(numbers, number)
	}
	slices.Sort(numbers)
	snapshot := make([]Event, 0, len(numbers))
	for _, number := range numbers {
		snapshot = append(snapshot, h.latest[number])
	}
	events := make(chan Event, 128)
	if h.final != nil {
		snapshot = append(snapshot, *h.final)
	}
	if h.closed {
		close(events)
		return snapshot, events, func() {}
	}
	h.subscribers[events] = struct{}{}
	cancel := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, found := h.subscribers[events]; found {
			delete(h.subscribers, events)
			close(events)
		}
	}
	return snapshot, events, cancel
}

// close tells every live stream to send what it already has and finish, so that
// the listener can shut down without cutting an event off in flight.
func (h *eventHub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	for subscriber := range h.subscribers {
		delete(h.subscribers, subscriber)
		close(subscriber)
	}
}

// handleEvents serves the live Ticket Attempts as a Server-Sent Events stream.
// It sends the latest event for every ticket on connect, then each new event as
// the Reporter reports it, so a dropped connection catches up on reconnect.
func (d *dashboard) handleEvents(token string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, token) || !readOnly(w, r) {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
			return
		}

		snapshot, events, cancel := d.events.subscribe()
		defer cancel()

		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		// A browser reconnects by itself; the snapshot it asks for next is
		// what catches it up.
		_, _ = io.WriteString(w, "retry: 1000\n\n")
		flusher.Flush()

		for _, e := range snapshot {
			if err := writeDashboardEvent(w, e); err != nil {
				return
			}
		}
		flusher.Flush()

		heartbeat := time.NewTicker(dashboardHeartbeat)
		defer heartbeat.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case e, open := <-events:
				if !open {
					return
				}
				if err := writeDashboardEvent(w, e); err != nil {
					return
				}
				flusher.Flush()
			case <-heartbeat.C:
				if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func writeDashboardEvent(w io.Writer, e Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
		return err
	}
	return nil
}
