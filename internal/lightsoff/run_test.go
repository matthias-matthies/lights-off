package lightsoff_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// day is a ticket's opening date, days after the first of November.
func day(n int) time.Time { return time.Date(2026, 11, 1+n, 9, 0, 0, 0, time.UTC) }

func TestRunPicksTheOldestEligibleTicketFirst(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(2)
	w.addTicket(43, day(0))
	w.addTicket(44, day(1))

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{43, 44}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
}

func TestRunSkipsClaimedAndBlockedTicketsWithANotice(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = []string{"ready-for-agent", "agent-in-progress"} // claimed by another run
	w.ticketOpened = day(0)
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#3"}
	w.issueStates["owner/repo#3"] = "OPEN"
	w.addTicket(44, day(2)).body = "Blocked by: the auth rework" // names no ticket
	w.addTicket(45, day(3))

	if code := w.run("owner/repo", "--n=1"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{45}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
	w.assertEventOnce(`^#42 skipped: .*already claimed`)
	w.assertEventOnce(`^#43 skipped: .*open blockers: #3`)
	w.assertEventOnce(`^#44 skipped: .*can't be resolved`)
}

func TestTicketUnblockedByAMergeEarlierInTheRunIsPickedUpInThatRun(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}
	w.addTicket(44, day(2)).blockedBy = []string{"owner/repo#3"}
	w.issueStates["owner/repo#3"] = "OPEN"

	if code := w.run("owner/repo", "--n=5"); code != 0 {
		t.Fatalf("exit code = %d, want 0 with fewer eligible than asked for\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{42, 43}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
	w.assertEventOnce(`^#44 skipped: .*open blockers: #3`)
	if !strings.Contains(w.stdout.String(), "2 of 5") {
		t.Errorf("stdout doesn't say only 2 of 5 tickets were eligible:\n%s", w.stdout.String())
	}
}

func TestRunContinuesAfterAFailureAndSkipsWhatItBlocks(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.implementerOutput = `{"status":"blocked","pr":0,"reason":"the API it needs doesn't exist"}`
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}
	w.addTicket(44, day(2))
	w.addTicket(45, day(3))

	// No terminal is attached, as under cron.
	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	// #42 failed and still counts, so #45 is beyond --n.
	if got, want := w.claimed(), []int{42, 44}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
	w.assertEventOnce(`^#42 ready for human`)
	w.assertEventOnce(`^#43 skipped: .*open blockers: #42`)
	w.assertEventOnce(`^#44 merged`)
}

func TestToolFailureStopsTheRun(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.mergeExit = 1
	w.hostMemory = 2 << 30 // one ticket at a time, so #43 would start only after #42
	w.addTicket(43, day(1))

	if code := w.run("owner/repo", "--n=2"); code == 0 {
		t.Errorf("exit code = 0, want non-zero after a Tool failure\nstdout:\n%s", w.stdout.String())
	}
	if got, want := w.claimed(), []int{42}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
}

func TestRunWithNothingEligibleExitsNormally(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = nil

	if code := w.run("owner/repo", "--n=3"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created: %v", got)
	}
	if !strings.Contains(w.stdout.String(), "0 of 3") {
		t.Errorf("stdout doesn't say no ticket was eligible:\n%s", w.stdout.String())
	}
}

func TestRunWaitsForGitHubToCloseAMergedTicket(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.closeDelay = 20 * time.Second
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{42, 43}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
}

func TestTicketGitHubLeavesOpenAfterTheMergeDoesNotStopTheRun(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.closeDelay = time.Hour
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}
	w.addTicket(44, day(2))

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{42, 44}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
	w.assertEventOnce(`^#42 still open .* after the merge`)
	w.assertEventOnce(`^#43 skipped: .*open blockers: #42`)
}

func TestOldestTicketIsPickedFromABacklogLargerThanOneListing(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	for i := range 1000 {
		w.addTicket(100+i, day(1).Add(time.Duration(i)*time.Minute))
	}

	if code := w.run("owner/repo", "--n=1"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got, want := w.claimed(), []int{42}; !slices.Equal(got, want) {
		t.Errorf("claimed %v, want %v", got, want)
	}
}
