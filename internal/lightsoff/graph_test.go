package lightsoff_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

type graphResponse struct {
	Repo    string        `json:"repo"`
	Tickets []graphTicket `json:"tickets"`
}

type graphTicket struct {
	Number       int            `json:"number"`
	Title        string         `json:"title"`
	Labels       []string       `json:"labels"`
	URL          string         `json:"url"`
	Blockers     []graphBlocker `json:"blockers"`
	Eligible     bool           `json:"eligible"`
	FailedChecks []string       `json:"failedChecks"`
}

type graphBlocker struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Open   bool   `json:"open"`
}

// TestDashboardGraphMatchesTheFakeWorld checks the endpoint against the same
// tickets, blockers and eligibility rules the rest of lights-off uses: every
// open ticket appears, a closed one doesn't, and each ineligible ticket carries
// the failed checks the --ticket prompt would list.
func TestDashboardGraphMatchesTheFakeWorld(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	// #42 is eligible, and its native blocker #45 is already closed.
	w.blockedBy = []string{"owner/repo#45"}
	closed := w.addTicket(45, time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC))
	closed.state = "CLOSED"

	// #43 is held by #42 through a native blocking link.
	held := w.addTicket(43, time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC))
	held.blockedBy = []string{"owner/repo#42"}

	// #44 has neither the label nor a native link; its blocker comes from text.
	textHeld := w.addTicket(44, time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC))
	textHeld.labels = nil
	textHeld.body = "Do 44.\n\n## Blocked by\n\n- #42\n"

	var got graphResponse
	var fetched bool
	code := w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		if c.Name == "sbx" && c.Args[0] == "run" && !fetched {
			fetched = true
			raw := w.openedURL()
			if raw == "" {
				t.Fatal("the dashboard did not start before the planning session")
			}
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("dashboard URL %q: %v", raw, err)
			}
			graphURL := *parsed
			graphURL.Path = "/graph"

			if calls := w.graphCalls(); calls != 0 {
				t.Errorf("gh calls for the graph before any page asked = %d, want 0", calls)
			}
			// The page itself must not make the server prefetch the graph; only
			// the page's own graph request may.
			status, page := get(t, raw)
			if status != http.StatusOK {
				t.Fatalf("GET page = %d, want %d", status, http.StatusOK)
			}
			if !strings.Contains(page, "POLL_INTERVAL = 60000") || !strings.Contains(page, "setInterval(fetchGraph, POLL_INTERVAL)") {
				t.Errorf("page does not fetch the graph every 60 seconds:\n%s", page)
			}
			if calls := w.graphCalls(); calls != 0 {
				t.Errorf("gh calls for the graph after serving the page but before the page asked = %d, want 0", calls)
			}

			noToken := graphURL
			noToken.RawQuery = ""
			if status, _ := get(t, noToken.String()); status != http.StatusForbidden {
				t.Errorf("GET graph without the token = %d, want %d", status, http.StatusForbidden)
			}
			if status := request(t, http.MethodPost, graphURL.String()); status != http.StatusMethodNotAllowed {
				t.Errorf("POST graph = %d, want %d", status, http.StatusMethodNotAllowed)
			}

			status, body := get(t, graphURL.String())
			if status != http.StatusOK {
				t.Fatalf("GET graph = %d, want %d:\n%s", status, http.StatusOK, body)
			}
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("graph JSON is unreadable: %v:\n%s", err, body)
			}
		}
		return w.exec(c)
	}, "plan", "owner/repo")
	if code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if !fetched {
		t.Fatal("the planning session never ran, so the graph was not checked")
	}

	if got.Repo != "owner/repo" {
		t.Errorf("graph repo = %q, want owner/repo", got.Repo)
	}
	byNumber := map[int]graphTicket{}
	for _, ticket := range got.Tickets {
		byNumber[ticket.Number] = ticket
	}
	if len(got.Tickets) != 3 {
		t.Fatalf("graph has %d tickets, want the 3 open ones: %+v", len(got.Tickets), got.Tickets)
	}
	if _, present := byNumber[45]; present {
		t.Errorf("closed ticket #45 is in the graph")
	}

	eligible := byNumber[42]
	if !eligible.Eligible || len(eligible.FailedChecks) != 0 {
		t.Errorf("#42 = %+v, want eligible with no failed checks", eligible)
	}
	if eligible.Title != w.ticketTitle || eligible.URL != "https://github.com/owner/repo/issues/42" {
		t.Errorf("#42 title/url = %q/%q", eligible.Title, eligible.URL)
	}
	if len(eligible.Labels) != 1 || eligible.Labels[0] != "ready-for-agent" {
		t.Errorf("#42 labels = %v, want [ready-for-agent]", eligible.Labels)
	}
	assertBlocker(t, eligible, "owner/repo", 45, false)

	heldTicket := byNumber[43]
	if heldTicket.Eligible {
		t.Errorf("#43 is eligible, want held back by #42")
	}
	if len(heldTicket.Labels) != 1 || heldTicket.Labels[0] != "ready-for-agent" {
		t.Errorf("#43 labels = %v, want [ready-for-agent]", heldTicket.Labels)
	}
	assertFailedChecks(t, heldTicket, "it has open blockers: #42")
	assertBlocker(t, heldTicket, "owner/repo", 42, true)

	textHeldTicket := byNumber[44]
	if textHeldTicket.Eligible {
		t.Errorf("#44 is eligible, want held back by its text blocker")
	}
	assertFailedChecks(t, textHeldTicket, "it has no ready-for-agent label", "it has open blockers: #42")
	assertBlocker(t, textHeldTicket, "owner/repo", 42, true)
}

// TestDashboardGraphFailureIsAnError checks that a gh failure while building the
// graph fails the request instead of returning a partial graph; the page then
// keeps what it already has and says the fetch failed.
func TestDashboardGraphFailureIsAnError(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	checked := false
	code := w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		if c.Name == "sbx" && c.Args[0] == "run" && !checked {
			checked = true
			raw := w.openedURL()
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("dashboard URL %q: %v", raw, err)
			}
			graphURL := *parsed
			graphURL.Path = "/graph"

			if status, _ := get(t, graphURL.String()); status != http.StatusOK {
				t.Fatalf("GET graph before the failure = %d, want %d", status, http.StatusOK)
			}
			w.mu.Lock()
			w.eventsExit = 1
			w.mu.Unlock()
			status, body := get(t, graphURL.String())
			if status != http.StatusBadGateway {
				t.Errorf("GET graph during a gh failure = %d, want %d:\n%s", status, http.StatusBadGateway, body)
			}
		}
		return w.exec(c)
	}, "plan", "owner/repo")
	if code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if !checked {
		t.Fatal("the planning session never ran")
	}
}

// TestDashboardDoesNotPollTheGraphOnItsOwn checks that the graph costs no gh
// calls while no page asks for it, including while the page is merely served.
func TestDashboardDoesNotPollTheGraphOnItsOwn(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	checked := false
	code := w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		if c.Name == "sbx" && c.Args[0] == "run" && !checked {
			checked = true
			if status, _ := get(t, w.openedURL()); status != http.StatusOK {
				t.Fatalf("GET page = %d, want %d", status, http.StatusOK)
			}
			if calls := w.graphCalls(); calls != 0 {
				t.Errorf("gh calls for the graph without a graph request = %d, want 0", calls)
			}
		}
		return w.exec(c)
	}, "plan", "owner/repo")
	if code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if !checked {
		t.Fatal("the planning session never ran")
	}
}

func assertBlocker(t *testing.T, ticket graphTicket, repo string, number int, open bool) {
	t.Helper()
	for _, blocker := range ticket.Blockers {
		if blocker.Repo == repo && blocker.Number == number {
			if blocker.Open != open {
				t.Errorf("#%d blocker #%d open = %v, want %v", ticket.Number, number, blocker.Open, open)
			}
			if blocker.URL != "https://github.com/owner/repo/issues/"+strconv.Itoa(number) {
				t.Errorf("#%d blocker #%d URL = %q", ticket.Number, number, blocker.URL)
			}
			return
		}
	}
	t.Errorf("#%d has no blocker %s#%d: %+v", ticket.Number, repo, number, ticket.Blockers)
}

func assertFailedChecks(t *testing.T, ticket graphTicket, want ...string) {
	t.Helper()
	if len(ticket.FailedChecks) != len(want) {
		t.Errorf("#%d failed checks = %v, want %v", ticket.Number, ticket.FailedChecks, want)
		return
	}
	for i := range want {
		if ticket.FailedChecks[i] != want[i] {
			t.Errorf("#%d failed checks = %v, want %v", ticket.Number, ticket.FailedChecks, want)
			return
		}
	}
}

// graphCalls counts the gh issue list calls the graph endpoint makes.
func (w *world) graphCalls() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for _, c := range w.commands {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "list" &&
			strings.Contains(strings.Join(c.Args, " "), "number,title,body,labels,url") {
			count++
		}
	}
	return count
}
