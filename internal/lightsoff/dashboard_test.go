package lightsoff_test

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

const browserTimeout = 2 * time.Second

// TestDashboardServesItsPageOnlyWithTheTokenWhilePlanRuns checks the listener
// while the command is still running, because that is when a person can use it.
func TestDashboardServesItsPageOnlyWithTheTokenWhilePlanRuns(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	var opened *url.URL
	code := w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		if c.Name == "sbx" && c.Args[0] == "run" {
			raw := w.openedURL()
			if raw == "" {
				t.Fatalf("no dashboard URL was opened before Claude Code started")
			}
			parsed, err := url.Parse(raw)
			if err != nil {
				t.Fatalf("dashboard URL %q: %v", raw, err)
			}
			opened = parsed
			if parsed.Hostname() != "127.0.0.1" {
				t.Errorf("dashboard listens on %q, want 127.0.0.1", parsed.Hostname())
			}
			if parsed.Port() == "" || parsed.Port() == "0" {
				t.Errorf("dashboard URL has no OS-chosen port: %q", raw)
			}

			status, body := get(t, raw)
			if status != http.StatusOK {
				t.Errorf("GET with the token = %d, want %d", status, http.StatusOK)
			}
			for _, want := range []string{"owner/repo", "plan", "lights-off"} {
				if !strings.Contains(body, want) {
					t.Errorf("dashboard page lacks %q:\n%s", want, body)
				}
			}

			withoutToken := *parsed
			withoutToken.RawQuery = ""
			if status, _ := get(t, withoutToken.String()); status != http.StatusForbidden {
				t.Errorf("GET without the token = %d, want %d", status, http.StatusForbidden)
			}

			wrongToken := *parsed
			query := wrongToken.Query()
			query.Set("token", "not-the-token")
			wrongToken.RawQuery = query.Encode()
			if status, _ := get(t, wrongToken.String()); status != http.StatusForbidden {
				t.Errorf("GET with the wrong token = %d, want %d", status, http.StatusForbidden)
			}

			if status := request(t, http.MethodPost, raw); status != http.StatusMethodNotAllowed {
				t.Errorf("POST with the token = %d, want %d: the dashboard has no write endpoints", status, http.StatusMethodNotAllowed)
			}
		}
		return w.exec(c)
	}, "plan", "owner/repo")
	if code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if opened == nil {
		t.Fatal("the planned session never ran, so the dashboard was not checked")
	}
	if got := w.openedURLs(); len(got) != 1 {
		t.Errorf("URLs opened = %v, want one", got)
	}
	if !strings.Contains(w.stdout.String(), opened.String()) {
		t.Errorf("stdout lacks the dashboard URL %q:\n%s", opened, w.stdout.String())
	}

	// The listener is process-scoped: once Run returns, the URL is dead.
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Get(opened.String())
	if err == nil {
		resp.Body.Close()
		t.Errorf("dashboard still answers after the command exited")
	}
}

func TestDashboardStartsForPlanNTicketAndOpensItInTheBrowser(t *testing.T) {
	cases := map[string]struct {
		args    []string
		command string
	}{
		"plan":     {[]string{"plan", "owner/repo"}, "lights-off plan owner/repo"},
		"--n":      {[]string{"owner/repo", "--n=1"}, "lights-off owner/repo --n=1"},
		"--ticket": {[]string{"owner/repo", "--ticket=42"}, "lights-off owner/repo --ticket=42"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.terminal = true
			checked := false
			code := w.runWithExec(func(command lightsoff.Command) (lightsoff.Result, error) {
				if !checked && w.openedURL() != "" {
					status, body := get(t, w.openedURL())
					if status != http.StatusOK {
						t.Errorf("GET with the token = %d, want %d", status, http.StatusOK)
					}
					for _, want := range []string{"Target Repository", "owner/repo", c.command} {
						if !strings.Contains(body, want) {
							t.Errorf("dashboard page lacks %q:\n%s", want, body)
						}
					}
					checked = true
				}
				return w.exec(command)
			}, c.args...)
			if code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
			}
			if !checked {
				t.Error("the dashboard was never reachable while the command ran")
			}
			urls := w.openedURLs()
			if len(urls) != 1 {
				t.Fatalf("URLs opened = %v, want one", urls)
			}
			if !strings.Contains(w.stdout.String(), urls[0]) {
				t.Errorf("stdout lacks the dashboard URL %q:\n%s", urls[0], w.stdout.String())
			}
		})
	}
}

func TestNoDashboardWithoutATerminalWithTheFlagOrWhenTheTicketIsDeclined(t *testing.T) {
	cases := map[string]struct {
		terminal   bool
		stdout     *bool
		input      string
		ineligible bool
		args       []string
	}{
		"stdout is not a terminal": {
			terminal: true, stdout: boolPointer(false), args: []string{"plan", "owner/repo"},
		},
		"no terminal": {
			args: []string{"owner/repo", "--n=1"},
		},
		"asked not to": {
			terminal: true, args: []string{"owner/repo", "--n=1", "--no-dashboard"},
		},
		"plan asked not to": {
			terminal: true, args: []string{"plan", "owner/repo", "--no-dashboard"},
		},
		"ticket confirmation declined": {
			terminal: true, input: "no\n", ineligible: true, args: []string{"owner/repo", "--ticket=42"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.terminal = c.terminal
			w.stdoutTerminal = c.stdout
			w.input = c.input
			if c.ineligible {
				w.ticketLabels = nil
			}

			w.run(c.args...)
			if got := w.openedURLs(); len(got) != 0 {
				t.Errorf("URLs opened = %v, want none", got)
			}
			if strings.Contains(w.stdout.String(), "http://127.0.0.1:") {
				t.Errorf("stdout names a dashboard URL:\n%s", w.stdout.String())
			}
		})
	}
}

// A confirmation must come before the Dashboard starts, not merely before work.
func TestTicketDashboardStartsAfterTheConfirmationIsAnsweredYes(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = nil
	w.confirm()

	w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		line := strings.Join(append([]string{c.Name}, c.Args...), " ")
		if strings.HasPrefix(line, "gh issue view 42 --repo owner/repo --json title,body,state,labels") && w.openedURL() != "" {
			t.Errorf("the dashboard started before the confirmation was answered")
		}
		if c.Name == "sbx" && c.Args[0] == "create" && w.openedURL() == "" {
			t.Errorf("the dashboard had not started after the confirmation was answered yes")
		}
		return w.exec(c)
	}, "owner/repo", "--ticket=42")

	if got := w.openedURLs(); len(got) != 1 {
		t.Errorf("URLs opened = %v, want one after the confirmation", got)
	}
	if !strings.Contains(w.stderr.String(), "[y/N]") {
		t.Errorf("the confirmation was not asked:\n%s", w.stderr.String())
	}
}

func TestDashboardThatCannotStartIsReportedAndTheCommandCarriesOn(t *testing.T) {
	cases := map[string]struct {
		args  []string
		after []string
	}{
		"plan":     {[]string{"plan", "owner/repo"}, []string{"sbx", "run"}},
		"--n":      {[]string{"owner/repo", "--n=1"}, []string{"gh", "pr", "merge"}},
		"--ticket": {[]string{"owner/repo", "--ticket=42"}, []string{"gh", "pr", "merge"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.terminal = true
			w.listen = func(string, string) (net.Listener, error) {
				return nil, errors.New("no socket for the dashboard")
			}

			if code := w.run(c.args...); code != 0 {
				t.Fatalf("exit code = %d, want 0: a dashboard failure is not a Tool failure\nstdout:\n%s\nstderr:\n%s",
					code, w.stdout.String(), w.stderr.String())
			}
			if got := w.openedURLs(); len(got) != 0 {
				t.Errorf("URLs opened = %v, want none", got)
			}
			if len(w.calls(c.after...)) == 0 {
				t.Errorf("the command did not carry on to %v: %v", c.after, w.commands)
			}
			w.assertStderrHas("dashboard", "no socket for the dashboard")
		})
	}
}

func boolPointer(v bool) *bool { return &v }

func get(t *testing.T, raw string) (int, string) {
	t.Helper()
	return do(t, http.MethodGet, raw)
}

func request(t *testing.T, method, raw string) int {
	t.Helper()
	status, _ := do(t, method, raw)
	return status
}

func do(t *testing.T, method, raw string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, raw, nil)
	if err != nil {
		t.Fatalf("%s %s: %v", method, raw, err)
	}
	client := &http.Client{Timeout: browserTimeout}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, raw, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", method, raw, err)
	}
	return resp.StatusCode, string(body)
}
