package lightsoff

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"time"
)

// dashboardPage is the whole page: one HTML file with its CSS and JavaScript
// inline, so that the binary needs no build step, CDN or dependency to serve it.
//
//go:embed dashboard.html
var dashboardPage string

var dashboardTemplate = template.Must(template.New("dashboard").Parse(dashboardPage))

const dashboardListenAddress = "127.0.0.1:0"

// dashboard is the HTTP listener a command carries while it runs.
type dashboard struct {
	server *http.Server
	url    string
	sh     shell
	events *eventHub
}

// startDashboard starts a Dashboard when the command should have one. A
// Dashboard that can't start is reported on stderr and returns nil, because it
// must never turn a command into a Tool failure.
func startDashboard(env Env, repo, command string, allowlist []string, wanted bool) *dashboard {
	if !wanted || !env.stdoutIsTerminal() {
		return nil
	}
	token, err := dashboardToken()
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: dashboard: %v\n", err)
		return nil
	}
	listen := net.Listen
	if env.Listen != nil {
		listen = env.Listen
	}
	listener, err := listen("tcp", dashboardListenAddress)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: dashboard: %v\n", err)
		return nil
	}
	d := &dashboard{url: fmt.Sprintf("http://%s/?token=%s", listener.Addr(), token), sh: shell{exec: env.Exec}, events: newEventHub()}
	mux := http.NewServeMux()
	mux.HandleFunc("/", d.handle(token, repo, command))
	graph := d.handleGraph(token, repo, allowlist)
	mux.HandleFunc("/graph", graph)
	// The page asks for /graph, but keep the namespaced spelling too, so an
	// existing bookmark or a hand-written request keeps working.
	mux.HandleFunc("/api/graph", graph)
	events := d.handleEvents(token)
	mux.HandleFunc("/events", events)
	mux.HandleFunc("/api/events", events)
	// Keep the older and plain spellings working too, so a hand-written
	// EventSource URL doesn't have to know which name the page picked.
	mux.HandleFunc("/stream", events)
	mux.HandleFunc("/api/stream", events)
	mux.HandleFunc("/attempts", events)
	mux.HandleFunc("/api/attempts", events)
	d.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = d.server.Serve(listener) }()

	fmt.Fprintf(env.Stdout, "lights-off: dashboard at %s\n", d.url)
	if env.OpenURL != nil {
		// The browser is a convenience; a failure to open it changes nothing.
		_ = env.OpenURL(d.url)
	}
	return d
}

// stdoutIsTerminal decides whether a Dashboard starts. It falls back to the
// stdin terminal flag for callers that don't distinguish the two.
func (e Env) stdoutIsTerminal() bool {
	if e.StdoutIsTerminal != nil {
		return e.StdoutIsTerminal()
	}
	return e.IsTerminal != nil && e.IsTerminal()
}

func dashboardToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("creating a token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// authorized answers only a request carrying the token.
func authorized(w http.ResponseWriter, r *http.Request, token string) bool {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

// readOnly allows only GET and HEAD, because the Dashboard has no write
// endpoints.
func readOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// handle serves the page, and only to a request carrying the token.
func (d *dashboard) handle(token, repo, command string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, token) || !readOnly(w, r) {
			return
		}
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		var page bytes.Buffer
		if err := dashboardTemplate.Execute(&page, struct {
			Repo    string
			Command string
		}{repo, command}); err != nil {
			http.Error(w, "dashboard page unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(page.Bytes())
	}
}

// handleGraph builds the ticket graph for one request. It is deliberately not
// cached or prefetched: lights-off asks gh for the graph only when a page asks,
// and a page that is closed makes no requests.
func (d *dashboard) handleGraph(token, repo string, allowlist []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorized(w, r, token) || !readOnly(w, r) {
			return
		}
		graph, err := d.sh.buildTicketGraph(repo, allowlist)
		if err != nil {
			http.Error(w, "ticket graph unavailable: "+err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(graph)
	}
}

// publish hands a structured Reporter event to the page. It is called from
// whatever goroutine reported it, including several Ticket Attempts at once.
func (d *dashboard) publish(e Event) {
	if d != nil && d.events != nil {
		d.events.publish(e)
	}
}

// Close stops the listener when the command ends. Live streams are told to
// finish first, so that an event already reported is not cut off mid-flight.
func (d *dashboard) Close() {
	if d == nil || d.server == nil {
		return
	}
	if d.events != nil {
		d.events.close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := d.server.Shutdown(ctx); err != nil {
		_ = d.server.Close()
	}
}

// dashboardCommand names the command the page is running.
func dashboardCommand(opts options) string {
	switch {
	case opts.plan:
		return "lights-off plan " + opts.repo
	case opts.n != 0:
		return fmt.Sprintf("lights-off %s --n=%d", opts.repo, opts.n)
	default:
		return fmt.Sprintf("lights-off %s --ticket=%d", opts.repo, opts.ticket)
	}
}
