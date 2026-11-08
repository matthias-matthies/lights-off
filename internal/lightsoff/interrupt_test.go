package lightsoff_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

// isAgent reports whether c runs an agent in a sandbox for ticket.
func isAgent(c lightsoff.Command, role string, ticket int) bool {
	return c.Name == "sbx" && len(c.Args) > 3 && c.Args[0] == "exec" && slices.Contains(c.Args, "dsh") &&
		strings.Contains(c.Args[3], "-"+role+"-") && sandboxTicket(c.Args[3]) == ticket
}

// assertStoppedNotRemoved checks that each sandbox was stopped and kept.
func (w *world) assertStoppedNotRemoved(names ...string) {
	w.t.Helper()
	for _, name := range names {
		if !w.stopped[name] {
			w.t.Errorf("sandbox %s was not stopped", name)
		}
	}
	w.assertSandboxesKept()
}

func TestCtrlCStopsTheAgentAndGivesTheTicketBack(t *testing.T) {
	w := newWorld(t)
	w.interruptAtOnce = 1 // during the Implementer's first pass, before any pull request

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C is no Tool failure\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	w.assertNoMerge()
	w.assertNotMovedToReadyForHuman()
	if len(w.sandboxes) != 1 {
		t.Fatalf("sandboxes = %v, want the Implementer's alone", w.sandboxes)
	}
	w.assertStoppedNotRemoved(w.sandboxes[0])
	if !slices.Contains(w.ticketLabels, "ready-for-agent") || slices.Contains(w.ticketLabels, "agent-in-progress") {
		t.Errorf("ticket labels = %v, want ready-for-agent back in place of agent-in-progress", w.ticketLabels)
	}
	w.assertEventOnce(`^#42 interrupted: back to ready-for-agent`)
	w.assertSummaryHas("interrupted: #42, back to ready-for-agent", "sandboxes kept: "+w.sandboxes[0])
}

func TestCtrlCLeavesATicketWithAPullRequestClaimed(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{redChecks, greenChecks}
	w.interruptWhen = func(c lightsoff.Command) bool { return isAgent(c, "implementer", 42) && w.implementerRuns == 1 }

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C is no Tool failure\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	w.assertNoMerge()
	w.assertNotMovedToReadyForHuman()
	w.assertStoppedNotRemoved(w.sandboxes...)
	if !slices.Contains(w.ticketLabels, "agent-in-progress") || slices.Contains(w.ticketLabels, "ready-for-agent") {
		t.Errorf("ticket labels = %v, want agent-in-progress kept", w.ticketLabels)
	}
	if len(w.prLabels) != 0 {
		t.Errorf("pull request labelled %v", w.prLabels)
	}
	w.assertEventOnce(`^#42 interrupted: pull request #7 stays open and the ticket keeps agent-in-progress`)
	w.assertSummaryHas("interrupted: #42, pull request #7 open, agent-in-progress kept")
}

func TestCtrlCAfterTheImplementerOpenedAPullRequestKeepsTheClaim(t *testing.T) {
	w := newWorld(t)
	w.prOpenedByKilledPass = true // it ran gh pr create, then Ctrl+C came before its answer
	w.interruptAtOnce = 1

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if !slices.Contains(w.ticketLabels, "agent-in-progress") || slices.Contains(w.ticketLabels, "ready-for-agent") {
		t.Errorf("ticket labels = %v, want agent-in-progress kept while pull request #7 is open", w.ticketLabels)
	}
	w.assertSummaryHas("interrupted: #42, pull request #7 open, agent-in-progress kept")
}

func TestCtrlCWhileWaitingForCIStopsWaiting(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{pendingChecks}
	polls := 0
	w.interruptWhen = func(c lightsoff.Command) bool {
		if c.Name == "gh" && slices.Equal(c.Args[:2], []string{"pr", "view"}) {
			polls++
		}
		return polls == 3
	}
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C is no Tool failure\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if waited := w.now.Sub(start); waited > 5*time.Minute {
		t.Errorf("waited %v after Ctrl+C", waited)
	}
	if got := w.calls("sbx", "exec"); slices.ContainsFunc(got, func(c lightsoff.Command) bool { return isAgent(c, "reviewer", 42) }) {
		t.Errorf("a Reviewer ran after Ctrl+C")
	}
	w.assertStoppedNotRemoved(w.sandboxes...)
	if !slices.Contains(w.ticketLabels, "agent-in-progress") {
		t.Errorf("ticket labels = %v, want agent-in-progress kept", w.ticketLabels)
	}
}

func TestCtrlCStopsEveryTicketRunningAtOnceAndStartsNoMore(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.addTicket(43, day(1))
	w.addTicket(44, day(2))
	w.hostMemory = 6 << 30 // two at once
	w.hold = 2
	w.interruptAtOnce = 2

	if code := w.run("owner/repo", "--n=3"); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C is no Tool failure\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{42, 43}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v", got, want)
	}
	w.assertNoMerge()
	w.assertStoppedNotRemoved(w.sandboxes...)
	if !slices.Contains(w.ticketLabels, "ready-for-agent") || !slices.Contains(w.others[43].labels, "ready-for-agent") {
		t.Errorf("labels: #42 %v, #43 %v, want ready-for-agent back on both", w.ticketLabels, w.others[43].labels)
	}
	w.assertSummaryHas("interrupted: #42, back to ready-for-agent", "interrupted: #43, back to ready-for-agent")
	if strings.Contains(w.stdout.String(), "no other ticket is eligible") {
		t.Errorf("an interrupted run reports a small backlog:\n%s", w.stdout.String())
	}
}

func TestCtrlCDuringThePeekIsNotAToolFailure(t *testing.T) {
	w := newWorld(t)
	w.hostMemory = 8 << 30 // three tickets at once
	w.addIndependentTickets(4)
	w.hold = 3 // the first three run together, so the fourth is peeked at
	w.holdFor = 2 * time.Second
	lists := 0
	w.interruptWhen = func(c lightsoff.Command) bool {
		if c.Name == "gh" && len(c.Args) > 1 && c.Args[0] == "issue" && c.Args[1] == "list" {
			lists++
		}
		return lists == 4 // the peek's listing, once the first three have started
	}

	if code := w.run("owner/repo", "--n=4"); code != 0 {
		t.Fatalf("exit code = %d, want 0: Ctrl+C during the peek is no Tool failure\nstdout:\n%s\nstderr:\n%s",
			code, w.stdout.String(), w.stderr.String())
	}
	if !w.interrupted {
		t.Fatal("Ctrl+C was not pressed during the peek")
	}
	if strings.Contains(w.stderr.String(), "interrupted by Ctrl+C") {
		t.Errorf("Ctrl+C during the peek was reported as a failure:\n%s", w.stderr.String())
	}
	if strings.Contains(w.stdout.String(), "no other ticket is eligible") {
		t.Errorf("an interrupted run reports a small backlog:\n%s", w.stdout.String())
	}
	w.assertStoppedNotRemoved(w.sandboxes...)
}

func TestCtrlCBeforeTheRunStartsIsLeftToTheShell(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = []string{"bug"}
	w.terminal = true
	w.input = "n\n"

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	if w.catching {
		t.Errorf("Ctrl+C caught at the --ticket prompt, where it should simply end lights-off")
	}
}

func TestPlanningSessionLeavesCtrlCToThePerson(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if w.catching {
		t.Errorf("Ctrl+C caught during a Planning Session, which needs it for Claude Code")
	}
}

func TestTicketLeftClaimedByACrashIsRecoveredWithTicket(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = []string{"agent-in-progress"} // what a claim leaves when lights-off dies
	w.confirm()

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	w.assertStderrHas("already claimed")
	w.assertEventOnce(`^#42 merged pull request #7`)
}
