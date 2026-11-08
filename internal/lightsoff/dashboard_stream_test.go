package lightsoff_test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

// dashboardStream reads one Server-Sent Events connection in the background, so
// a test can watch a Ticket Attempt while the command is still running.
type dashboardStream struct {
	ctx    context.Context
	cancel context.CancelFunc
	body   io.ReadCloser
	events chan lightsoff.Event
	errs   chan error
}

func openDashboardStream(t *testing.T, dashboardURL string) *dashboardStream {
	t.Helper()
	parsed, err := url.Parse(dashboardURL)
	if err != nil {
		t.Fatalf("dashboard URL %q: %v", dashboardURL, err)
	}
	streamURL := *parsed
	streamURL.Path = "/events"
	query := streamURL.Query()
	query.Set("token", parsed.Query().Get("token"))
	streamURL.RawQuery = query.Encode()

	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, streamURL.String(), nil)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", streamURL.String(), err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("GET %s: %v", streamURL.String(), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		t.Fatalf("GET stream = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		resp.Body.Close()
		cancel()
		t.Fatalf("stream Content-Type = %q, want text/event-stream", got)
	}
	s := &dashboardStream{ctx: ctx, cancel: cancel, body: resp.Body, events: make(chan lightsoff.Event, 64), errs: make(chan error, 1)}
	go s.read()
	t.Cleanup(s.close)
	return s
}

func (s *dashboardStream) read() {
	defer close(s.events)
	scanner := bufio.NewScanner(s.body)
	var data []string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		case line == "" && len(data) > 0:
			var e lightsoff.Event
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e); err != nil {
				select {
				case s.errs <- err:
				case <-s.ctx.Done():
				}
				return
			}
			select {
			case s.events <- e:
			case <-s.ctx.Done():
				return
			}
			data = nil
		}
	}
	if err := scanner.Err(); err != nil && s.ctx.Err() == nil {
		select {
		case s.errs <- err:
		case <-s.ctx.Done():
		}
	}
}

func (s *dashboardStream) next(t *testing.T) lightsoff.Event {
	t.Helper()
	// The connection closing after the command ends is normal. Read every
	// buffered event first, and only then report a read error.
	for {
		select {
		case e, open := <-s.events:
			if !open {
				select {
				case err := <-s.errs:
					t.Fatalf("reading the event stream: %v", err)
				default:
				}
				t.Fatal("the event stream closed")
			}
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a Ticket Attempt event")
		}
	}
}

func (s *dashboardStream) until(t *testing.T, match func(lightsoff.Event) bool) []lightsoff.Event {
	t.Helper()
	var events []lightsoff.Event
	for {
		e := s.next(t)
		events = append(events, e)
		if match(e) {
			return events
		}
	}
}

func (s *dashboardStream) close() {
	s.cancel()
	_ = s.body.Close()
}

func isIssueList(c lightsoff.Command) bool {
	return c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "issue" && c.Args[1] == "list"
}

// startStreamedRun holds the run at its first ticket list until the stream is
// connected, so the test sees the Attempt from its Queued event onwards.
func startStreamedRun(t *testing.T, w *world, args ...string) (<-chan int, *dashboardStream) {
	t.Helper()
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	var once sync.Once
	go func() {
		done <- w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
			if isIssueList(c) {
				once.Do(func() {
					close(started)
					<-release
				})
			}
			return w.exec(c)
		}, args...)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached its ticket list")
	}
	stream := openDashboardStream(t, w.openedURL())
	close(release)
	return done, stream
}

func waitExitCode(t *testing.T, done <-chan int) int {
	t.Helper()
	select {
	case code := <-done:
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("the run did not finish")
	}
	return 0
}

func streamPath(t *testing.T, dashboardURL, path string, withToken bool) string {
	t.Helper()
	parsed, err := url.Parse(dashboardURL)
	if err != nil {
		t.Fatalf("dashboard URL %q: %v", dashboardURL, err)
	}
	parsed.Path = path
	if !withToken {
		parsed.RawQuery = ""
	}
	return parsed.String()
}

// TestDashboardEventStreamSendsEveryAttemptPhaseInOrder scripts one Ticket
// Attempt and watches the same events the status lines are made from: the
// stream starts Queued, runs through Implementing, Waiting on CI and Reviewing,
// and ends Merged, carrying the Iteration, Verdict and pull request.
func TestDashboardEventStreamSendsEveryAttemptPhaseInOrder(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.checks = []string{pendingChecks, pendingChecks, greenChecks}

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	events := stream.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventMerged })
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	var phases []lightsoff.AttemptPhase
	for _, e := range events {
		if e.Ticket != 42 {
			t.Errorf("stream event %q belongs to ticket %d, want 42: %+v", e.Kind, e.Ticket, e)
		}
		if len(phases) == 0 || phases[len(phases)-1] != e.Phase {
			phases = append(phases, e.Phase)
		}
	}
	want := []lightsoff.AttemptPhase{
		lightsoff.PhaseQueued,
		lightsoff.PhaseImplementing,
		lightsoff.PhaseWaitingOnCI,
		lightsoff.PhaseReviewing,
		lightsoff.PhaseMerged,
	}
	if !slices.Equal(phases, want) {
		t.Errorf("stream showed phases %v, want %v", phases, want)
	}

	first := events[0]
	if first.Kind != lightsoff.EventQueued || first.Iteration != 1 {
		t.Errorf("first stream event = %+v, want Queued iteration 1", first)
	}
	var waiting, merged *lightsoff.Event
	for i := range events {
		switch events[i].Kind {
		case lightsoff.EventWaitingForCI:
			waiting = &events[i]
		case lightsoff.EventMerged:
			merged = &events[i]
		}
	}
	if waiting == nil || waiting.PullRequest != 7 || waiting.Iteration != 1 || waiting.Phase != lightsoff.PhaseWaitingOnCI {
		t.Errorf("waiting event = %+v, want pull request 7 in iteration 1 of %q", waiting, lightsoff.PhaseWaitingOnCI)
	}
	if merged == nil {
		t.Fatal("no merged event")
	}
	if merged.PullRequest != 7 || merged.Iteration != 1 || merged.Phase != lightsoff.PhaseMerged {
		t.Errorf("merged event = %+v, want pull request 7 in iteration 1 of %q", merged, lightsoff.PhaseMerged)
	}
	if merged.Verdict == nil || !merged.Verdict.Mergeable || merged.Verdict.Risk != "low" || merged.Verdict.Update != "two-way" {
		t.Errorf("merged event verdict = %+v, want the Reviewer's mergeable low-risk Two-way verdict", merged.Verdict)
	}
	if merged.Sandbox == "" {
		t.Errorf("merged event names no sandbox: %+v", merged)
	}
}

// TestDashboardEventStreamEndsWithTheSummary watches an Autonomous Run to its
// last event: the summary arrives before the Dashboard stops, it names what
// merged, and nothing follows it on the stream.
func TestDashboardEventStreamEndsWithTheSummary(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	var events []lightsoff.Event
	for {
		e := stream.next(t)
		events = append(events, e)
		if e.Kind == lightsoff.EventSummary {
			break
		}
	}
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	// The stream ends with the summary, not merely carries it somewhere.
	select {
	case e, open := <-stream.events:
		if open {
			t.Errorf("stream sent %q after the summary: %+v", e.Kind, e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream did not close after the summary")
	}

	summary := events[len(events)-1]
	if summary.Ticket != 0 {
		t.Errorf("summary event belongs to ticket %d, want none", summary.Ticket)
	}
	for _, want := range []string{"lights-off: summary", "merged: #42, pull request #7", "logs: "} {
		if !strings.Contains(summary.Summary, want) {
			t.Errorf("summary event lacks %q:\n%s", want, summary.Summary)
		}
	}
}

func TestDashboardEventStreamEndsWithTheSummaryForOneTicket(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	var once sync.Once
	go func() {
		done <- w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
			if c.Name == "sbx" && len(c.Args) > 0 && c.Args[0] == "create" {
				once.Do(func() {
					close(started)
					<-release
				})
			}
			return w.exec(c)
		}, "owner/repo", "--ticket=42")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached its sandbox")
	}
	stream := openDashboardStream(t, w.openedURL())
	close(release)

	events := stream.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventSummary })
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	summary := events[len(events)-1]
	for _, want := range []string{"lights-off: summary", "merged: #42, pull request #7"} {
		if !strings.Contains(summary.Summary, want) {
			t.Errorf("summary event lacks %q:\n%s", want, summary.Summary)
		}
	}
}

// TestDashboardEventStreamEndsWithTheSummaryAfterCtrlC checks the interrupted
// run still finishes the stream with the summary before the Dashboard stops.
func TestDashboardEventStreamEndsWithTheSummaryAfterCtrlC(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.interruptAtOnce = 1

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	events := stream.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventSummary })
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C is no Tool failure\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	summary := events[len(events)-1]
	if !strings.Contains(summary.Summary, "interrupted: #42, back to ready-for-agent") {
		t.Errorf("summary event lacks the interrupted ticket:\n%s", summary.Summary)
	}
}

// TestDashboardEventStreamEndsAPlanningSession holds the person's session open
// until the stream is connected, then ends it and watches for the last event.
func TestDashboardEventStreamEndsAPlanningSession(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	atSession, release := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	var once sync.Once
	go func() {
		done <- w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
			if c.Name == "sbx" && len(c.Args) > 0 && c.Args[0] == "run" {
				once.Do(func() {
					close(atSession)
					<-release
				})
			}
			return w.exec(c)
		}, "plan", "owner/repo")
	}()
	select {
	case <-atSession:
	case <-time.After(5 * time.Second):
		t.Fatal("the Planning Session never opened")
	}
	stream := openDashboardStream(t, w.openedURL())
	close(release)

	events := stream.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventSessionEnded })
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	ended := events[len(events)-1]
	if ended.Ticket != 0 {
		t.Errorf("session-ended event belongs to ticket %d, want none", ended.Ticket)
	}
	select {
	case e, open := <-stream.events:
		if open {
			t.Errorf("stream sent %q after session-ended: %+v", e.Kind, e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the event stream did not close after session-ended")
	}
}

func TestDashboardEventStreamRequiresTheTokenAndOnlyReads(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	checked := false
	code := w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
		if isIssueList(c) && !checked {
			checked = true
			raw := w.openedURL()
			if raw == "" {
				t.Fatal("the dashboard did not start")
			}
			withoutToken := streamPath(t, raw, "/events", false)
			if status, _ := get(t, withoutToken); status != http.StatusForbidden {
				t.Errorf("GET stream without the token = %d, want %d", status, http.StatusForbidden)
			}
			withToken := streamPath(t, raw, "/events", true)
			if status := request(t, http.MethodPost, withToken); status != http.StatusMethodNotAllowed {
				t.Errorf("POST stream = %d, want %d", status, http.StatusMethodNotAllowed)
			}
			// The namespaced spelling is kept for a hand-written request.
			namespaced := streamPath(t, raw, "/api/events", false)
			if status, _ := get(t, namespaced); status != http.StatusForbidden {
				t.Errorf("GET /api/events without the token = %d, want %d", status, http.StatusForbidden)
			}
		}
		return w.exec(c)
	}, "owner/repo", "--n=1")
	if code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if !checked {
		t.Fatal("the dashboard was never reachable")
	}
}

// TestDashboardEventStreamReconnectCatchesUpAndContinues drops a stream during
// the Attempt and reconnects while the Reviewing event is still the latest; the
// new connection receives that state at once and then the merge.
func TestDashboardEventStreamReconnectCatchesUpAndContinues(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	started, release := make(chan struct{}), make(chan struct{})
	atMerge, continueMerge := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	var listOnce sync.Once
	go func() {
		done <- w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
			if isIssueList(c) {
				listOnce.Do(func() {
					close(started)
					<-release
				})
			}
			if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "pr" && c.Args[1] == "merge" {
				close(atMerge)
				<-continueMerge
			}
			return w.exec(c)
		}, "owner/repo", "--n=1")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached its ticket list")
	}
	first := openDashboardStream(t, w.openedURL())
	close(release)

	select {
	case <-atMerge:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached the merge")
	}
	first.close()
	second := openDashboardStream(t, w.openedURL())
	latest := second.next(t)
	if latest.Kind != lightsoff.EventVerdict || latest.Phase != lightsoff.PhaseReviewing {
		t.Errorf("the reconnected stream began with %+v, want the latest Reviewing verdict", latest)
	}
	if latest.PullRequest != 7 || latest.Iteration != 1 || latest.Verdict == nil {
		t.Errorf("the reconnected snapshot lost the Attempt state: %+v", latest)
	}
	close(continueMerge)
	events := second.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventMerged })

	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	merged := events[len(events)-1]
	if merged.PullRequest != 7 || merged.Verdict == nil {
		t.Errorf("after reconnecting, merged event = %+v, want carry-over Verdict and pull request 7", merged)
	}
}

func TestDashboardEventStreamShowsHandBackThenLaterIteration(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.reviewerPasses = []agentPass{{output: rejectVerdict}, {output: mergeVerdict}}

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	events := stream.until(t, func(e lightsoff.Event) bool {
		return e.Kind == lightsoff.EventMerged && e.Iteration == 2
	})
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	handBack, resumed, verdictTwo, merged := -1, -1, -1, -1
	for i, e := range events {
		switch {
		case e.Kind == lightsoff.EventHandedBack:
			handBack = i
			if e.Iteration != 2 || e.Phase != lightsoff.PhaseImplementing {
				t.Errorf("hand-back = %+v, want the next Iteration 2 in %q", e, lightsoff.PhaseImplementing)
			}
			if !strings.Contains(e.Reason, "sum subtracts") || e.PullRequest != 7 {
				t.Errorf("hand-back = %+v, want its reason and pull request 7", e)
			}
			if e.ReasonKind != lightsoff.ReasonHandBack {
				t.Errorf("hand-back reason kind = %q, want %q", e.ReasonKind, lightsoff.ReasonHandBack)
			}
			if e.Verdict != nil {
				t.Errorf("hand-back still shows the previous Iteration's Verdict: %+v", e.Verdict)
			}
		case e.Kind == lightsoff.EventImplementerResumed && e.Iteration == 2:
			resumed = i
		case e.Kind == lightsoff.EventVerdict && e.Iteration == 2:
			verdictTwo = i
		case e.Kind == lightsoff.EventMerged && e.Iteration == 2:
			merged = i
		}
	}
	if handBack < 0 || resumed < 0 || verdictTwo < 0 || merged < 0 {
		t.Fatalf("stream events = %+v, want a Hand-back, a resumed Implementer, a second Verdict and a merged Iteration 2", events)
	}
	if !(handBack < resumed && resumed < verdictTwo && verdictTwo < merged) {
		t.Errorf("Hand-back at %d, resumed at %d, second Verdict at %d, merged at %d; want that order", handBack, resumed, verdictTwo, merged)
	}
	if events[merged].Verdict == nil || events[merged].Verdict.Reason != "fine" {
		t.Errorf("merged iteration 2 verdict = %+v, want the later Reviewer's verdict", events[merged].Verdict)
	}
	if events[verdictTwo].Reason != "fine" {
		t.Errorf("iteration 2 Verdict reason = %q, want its own reason", events[verdictTwo].Reason)
	}
	// The Hand-back explains why iteration 2 runs, so every event of that
	// Iteration shows its reason until its own Reviewer answers. None of those
	// events may keep showing iteration 1's Verdict as the current one.
	for _, e := range events[handBack:verdictTwo] {
		if !strings.Contains(e.Reason, "sum subtracts") {
			t.Errorf("%q event in iteration %d lost the Hand-back reason: %+v", e.Kind, e.Iteration, e)
		}
		if e.ReasonKind != lightsoff.ReasonHandBack {
			t.Errorf("%q event has reason kind %q, want %q", e.Kind, e.ReasonKind, lightsoff.ReasonHandBack)
		}
		if e.Verdict != nil {
			t.Errorf("%q event in iteration %d still shows the previous Iteration's Verdict: %+v", e.Kind, e.Iteration, e.Verdict)
		}
	}
	// Once iteration 2's Reviewer has answered, the older Hand-back is no
	// longer the current reason.
	for _, e := range events[verdictTwo:] {
		if strings.Contains(e.Reason, "sum subtracts") {
			t.Errorf("%q event still carries the replaced Hand-back reason %q", e.Kind, e.Reason)
		}
	}
	// The resumed Implementer is the event a reconnecting page would receive
	// while iteration 2 runs, so it must carry both parts of the state.
	if e := events[resumed]; e.Verdict != nil || !strings.Contains(e.Reason, "sum subtracts") {
		t.Errorf("resumed Implementer = %+v, want the Hand-back reason without the old Verdict", e)
	}
}

func TestDashboardEventStreamShowsWhyATicketWasSkipped(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.ticketOpened = day(1)
	held := w.addTicket(43, day(0))
	held.blockedBy = []string{"owner/repo#42"} // older than #42, but held by it

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	var skipped, merged *lightsoff.Event
	for {
		e := stream.next(t)
		switch {
		case e.Kind == lightsoff.EventSkipped && e.Ticket == 43:
			skipped = &e
		case e.Kind == lightsoff.EventMerged && e.Ticket == 42:
			merged = &e
		}
		if skipped != nil && merged != nil {
			break
		}
	}
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	if skipped.Phase != "" {
		t.Errorf("skipped #43 has phase %q, want none", skipped.Phase)
	}
	if !strings.Contains(skipped.Reason, "open blockers: #42") {
		t.Errorf("skipped #43 reason = %q, want its open blocker", skipped.Reason)
	}
}

// TestDashboardEventStreamKeepsAReadyForHumanReasonOnTheLatestState checks the
// reason a ticket waits for a person survives the sandbox events that follow
// it, so a page that replaces a ticket's state from the latest event still
// shows why it stopped.
func TestDashboardEventStreamKeepsAReadyForHumanReasonOnTheLatestState(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.reviewerOutput = oneWayVerdict

	done, stream := startStreamedRun(t, w, "owner/repo", "--n=1")
	var ready []lightsoff.Event
	var kept []lightsoff.Event
	for {
		e := stream.next(t)
		switch e.Kind {
		case lightsoff.EventReadyForHuman:
			ready = append(ready, e)
		case lightsoff.EventSandboxKept:
			kept = append(kept, e)
		}
		if len(kept) == 2 {
			break
		}
	}
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if len(ready) != 1 || ready[0].Reason != "drops a table" {
		t.Fatalf("ready-for-human events = %+v, want one with the Verdict's reason", ready)
	}
	for _, e := range kept {
		if e.Phase != lightsoff.PhaseReadyForHuman {
			t.Errorf("sandbox-kept event has phase %q, want %q", e.Phase, lightsoff.PhaseReadyForHuman)
		}
		if e.Reason != "drops a table" {
			t.Errorf("sandbox-kept event lost the ready-for-human reason: %+v", e)
		}
	}
}

// TestDashboardEventStreamReconnectKeepsAHandBackReason reconnects after a
// Hand-back has put the Attempt back into iteration 2. The snapshot must show
// the running state with the Hand-back that started it, and without the
// previous Iteration's Verdict.
func TestDashboardEventStreamReconnectKeepsAHandBackReason(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.reviewerPasses = []agentPass{{output: rejectVerdict}, {output: mergeVerdict}}

	started, release := make(chan struct{}), make(chan struct{})
	atIterationTwo, letRun := make(chan struct{}), make(chan struct{})
	done := make(chan int, 1)
	var listOnce, blockOnce sync.Once
	var reviewed atomic.Bool
	go func() {
		done <- w.runWithExec(func(c lightsoff.Command) (lightsoff.Result, error) {
			if isIssueList(c) {
				listOnce.Do(func() {
					close(started)
					<-release
				})
			}
			if c.Name == "gh" && len(c.Args) >= 3 && c.Args[0] == "pr" && c.Args[1] == "comment" {
				reviewed.Store(true)
			}
			if c.Name == "sbx" && len(c.Args) > 3 && c.Args[0] == "exec" &&
				strings.Contains(c.Args[3], "-implementer-") && reviewed.Load() {
				blockOnce.Do(func() {
					close(atIterationTwo)
					<-letRun
				})
			}
			return w.exec(c)
		}, "owner/repo", "--n=1")
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached its ticket list")
	}
	first := openDashboardStream(t, w.openedURL())
	close(release)
	first.until(t, func(e lightsoff.Event) bool { return e.Kind == lightsoff.EventHandedBack })

	select {
	case <-atIterationTwo:
	case <-time.After(5 * time.Second):
		t.Fatal("the run never reached the second Iteration's Implementer")
	}
	first.close()

	second := openDashboardStream(t, w.openedURL())
	latest := second.next(t)
	if latest.Kind != lightsoff.EventImplementerResumed || latest.Iteration != 2 || latest.Phase != lightsoff.PhaseImplementing {
		t.Errorf("the reconnected stream began with %+v, want the resumed Implementer in iteration 2", latest)
	}
	if !strings.Contains(latest.Reason, "sum subtracts") || latest.ReasonKind != lightsoff.ReasonHandBack {
		t.Errorf("the reconnected stream lost the Hand-back reason: %+v", latest)
	}
	if latest.Verdict != nil {
		t.Errorf("the reconnected stream still shows iteration 1's Verdict as current: %+v", latest.Verdict)
	}
	close(letRun)
	events := second.until(t, func(e lightsoff.Event) bool {
		return e.Kind == lightsoff.EventMerged && e.Iteration == 2
	})
	if code := waitExitCode(t, done); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	merged := events[len(events)-1]
	if merged.Reason != "" {
		t.Errorf("the merged event still carries the Hand-back reason %q: %+v", merged.Reason, merged)
	}
	if merged.Verdict == nil || merged.Verdict.Reason != "fine" {
		t.Errorf("the merged event verdict = %+v, want iteration 2's own Verdict", merged.Verdict)
	}
}
