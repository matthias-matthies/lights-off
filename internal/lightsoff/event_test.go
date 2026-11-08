package lightsoff_test

import (
	"strings"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

func TestEveryTicketEventCarriesItsAttemptPhaseAndIteration(t *testing.T) {
	w := newWorld(t)
	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if len(w.reportedEvents) == 0 {
		t.Fatal("no structured events reported")
	}
	for _, e := range w.reportedEvents {
		// The summary ends the run rather than describing a Ticket Attempt.
		if e.Kind == lightsoff.EventSummary || e.Kind == lightsoff.EventSessionEnded {
			continue
		}
		if e.Ticket != 42 {
			t.Errorf("event %q belongs to ticket %d, want 42: %+v", e.Kind, e.Ticket, e)
		}
		if e.Kind == lightsoff.EventSkipped {
			if e.Phase != "" {
				t.Errorf("a skipped ticket has Attempt Phase %q, want none", e.Phase)
			}
			continue
		}
		if e.Phase == "" {
			t.Errorf("event %q has no Attempt Phase: %+v", e.Kind, e)
		}
		if e.Iteration < 1 {
			t.Errorf("event %q has Iteration %d, want one or more: %+v", e.Kind, e.Iteration, e)
		}
	}

	claim, ok := w.eventFor(lightsoff.EventClaimed, 42)
	if !ok {
		t.Fatalf("no claimed event:\n%s", w.stdout.String())
	}
	if claim.Phase != lightsoff.PhaseImplementing || claim.Iteration != 1 {
		t.Errorf("claimed = phase %q iteration %d, want %q 1", claim.Phase, claim.Iteration, lightsoff.PhaseImplementing)
	}

	implementer, ok := w.eventFor(lightsoff.EventImplementerStarted, 42)
	if !ok || implementer.Sandbox == "" {
		t.Fatalf("implementer started event = %+v, want its sandbox", implementer)
	}
	if implementer.Phase != lightsoff.PhaseImplementing || implementer.Iteration != 1 {
		t.Errorf("implementer started = phase %q iteration %d, want %q 1", implementer.Phase, implementer.Iteration, lightsoff.PhaseImplementing)
	}

	ci, ok := w.eventFor(lightsoff.EventCIGreen, 42)
	if !ok || ci.PullRequest != 7 || ci.Phase != lightsoff.PhaseReviewing {
		t.Errorf("CI green event = %+v, want pull request 7 in %q", ci, lightsoff.PhaseReviewing)
	}

	reviewer, ok := w.eventFor(lightsoff.EventReviewerStarted, 42)
	if !ok || reviewer.Sandbox == "" {
		t.Fatalf("reviewer started event = %+v, want its sandbox", reviewer)
	}
	if reviewer.Phase != lightsoff.PhaseReviewing || reviewer.Iteration != 1 {
		t.Errorf("reviewer started = phase %q iteration %d, want %q 1", reviewer.Phase, reviewer.Iteration, lightsoff.PhaseReviewing)
	}

	verdict, ok := w.eventFor(lightsoff.EventVerdict, 42)
	if !ok || verdict.Verdict == nil {
		t.Fatalf("verdict event = %+v, want the structured Verdict", verdict)
	}
	if verdict.Phase != lightsoff.PhaseReviewing || verdict.PullRequest != 7 {
		t.Errorf("verdict = pull request %d in %q, want 7 in %q", verdict.PullRequest, verdict.Phase, lightsoff.PhaseReviewing)
	}
	if verdict.Verdict.Risk != "low" || verdict.Verdict.Update != "two-way" || !verdict.Verdict.Mergeable {
		t.Errorf("verdict carries %+v, want the Reviewer's answer", verdict.Verdict)
	}

	merged, ok := w.eventFor(lightsoff.EventMerged, 42)
	if !ok || merged.PullRequest != 7 || merged.Phase != lightsoff.PhaseMerged {
		t.Errorf("merged event = %+v, want pull request 7 in %q", merged, lightsoff.PhaseMerged)
	}
}

func TestSkippedTicketEventHasAReasonAndNoAttemptPhase(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}
	w.addTicket(44, day(2))

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	skipped, ok := w.eventFor(lightsoff.EventSkipped, 43)
	if !ok {
		t.Fatalf("no skipped event for #43:\n%s", w.stdout.String())
	}
	if skipped.Phase != "" {
		t.Errorf("skipped #43 has Attempt Phase %q, want none", skipped.Phase)
	}
	if !strings.Contains(skipped.Reason, "open blockers: #42") {
		t.Errorf("skipped #43 reason = %q, want its open blocker", skipped.Reason)
	}
	// The status line is unchanged; the Dashboard learns it is waiting, not gone.
	w.assertEventOnce(`^#43 skipped: .*open blockers: #42`)

	queued, ok := w.eventFor(lightsoff.EventQueued, 43)
	if !ok {
		t.Fatalf("no queued event for the blocked #43")
	}
	if queued.Phase != lightsoff.PhaseQueued || !strings.Contains(queued.Reason, "open blockers: #42") {
		t.Errorf("queued #43 = %+v, want %q with its blocker as the reason", queued, lightsoff.PhaseQueued)
	}
	for line := range strings.SplitSeq(w.stdout.String(), "\n") {
		if strings.HasPrefix(line, "#") && strings.Contains(strings.ToLower(line), "queued") {
			t.Errorf("a queued event produced a status line: %q", line)
		}
	}
}

func TestTicketWaitingForASlotEmitsAQueuedEventWithoutAStatusLine(t *testing.T) {
	w := newWorld(t)
	w.hostMemory = 8 << 30 // three tickets at once
	w.addIndependentTickets(4)
	w.hold = 3 // the first three run together, so the fourth waits for a slot
	w.holdFor = 100 * time.Millisecond

	if code := w.run("owner/repo", "--n=4"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	var waiting *lightsoff.Event
	for _, e := range w.eventsOf(lightsoff.EventQueued) {
		if e.Reason == "waiting for a free slot" {
			waiting = &e
			break
		}
	}
	if waiting == nil {
		t.Fatalf("no ticket was reported waiting for a free slot:\n%s", w.stdout.String())
	}
	if waiting.Phase != lightsoff.PhaseQueued {
		t.Errorf("waiting ticket phase = %q, want %q", waiting.Phase, lightsoff.PhaseQueued)
	}
	for line := range strings.SplitSeq(w.stdout.String(), "\n") {
		if strings.HasPrefix(line, "#") && strings.Contains(strings.ToLower(line), "queued") {
			t.Errorf("a queued event produced a status line: %q", line)
		}
	}
}

func TestFinalHandBackReportsTheEndingIterationAsReadyForHuman(t *testing.T) {
	w := newWorld(t)
	w.reviewerOutput = rejectVerdict

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	handBacks := w.eventsOf(lightsoff.EventHandedBack)
	if len(handBacks) != 3 {
		t.Fatalf("hand-back events = %d, want 3:\n%s", len(handBacks), w.stdout.String())
	}
	final := handBacks[2]
	if final.Phase != lightsoff.PhaseReadyForHuman {
		t.Errorf("final hand-back phase = %q, want %q: %+v", final.Phase, lightsoff.PhaseReadyForHuman, final)
	}
	if final.Iteration != 3 {
		t.Errorf("final hand-back iteration = %d, want the ending Iteration 3: %+v", final.Iteration, final)
	}
	w.assertEventOnce(`^#42 handed back after iteration 3 of 3: `)
	w.assertEventOnce(`^#42 ready for human: `)
}

func TestRedCIIterationReturnsToImplementingWithoutReviewing(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{redChecks, greenChecks}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	for _, e := range w.eventsOf(lightsoff.EventReviewerStarted) {
		if e.Iteration == 1 {
			t.Errorf("iteration 1 was reviewed after red CI: %+v", e)
		}
	}
	var handBack *lightsoff.Event
	for _, e := range w.eventsOf(lightsoff.EventHandedBack) {
		if strings.Contains(e.Reason, "CI failed") {
			handBack = &e
			break
		}
	}
	if handBack == nil {
		t.Fatalf("red CI was not handed back:\n%s", w.stdout.String())
	}
	if handBack.Phase != lightsoff.PhaseImplementing {
		t.Errorf("red CI hand-back phase = %q, want %q", handBack.Phase, lightsoff.PhaseImplementing)
	}
	if handBack.Iteration != 2 {
		t.Errorf("red CI hand-back iteration = %d, want the next Iteration 2", handBack.Iteration)
	}
	reviewer, ok := w.eventFor(lightsoff.EventReviewerStarted, 42)
	if !ok || reviewer.Iteration != 2 || reviewer.Phase != lightsoff.PhaseReviewing {
		t.Errorf("reviewer after the hand-back = %+v, want iteration 2 in %q", reviewer, lightsoff.PhaseReviewing)
	}
}
