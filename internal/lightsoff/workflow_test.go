package lightsoff_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestWorkflowChangeEndsTheAttemptAtAPersonAfterOneIteration(t *testing.T) {
	w := newWorld(t)
	w.implementerFiles = [][]string{{
		"main.go",
		".github/workflows/release.yml",
		".github/workflows/ci.yaml",
	}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	if w.implementerRuns != 1 {
		t.Errorf("implementer passes = %d, want 1", w.implementerRuns)
	}
	if w.reviewerRuns != 0 {
		t.Errorf("reviewers = %d, want none for work a person must change", w.reviewerRuns)
	}
	if len(w.sandboxes) != 1 {
		t.Errorf("sandboxes = %v, want only the Implementer's", w.sandboxes)
	}
	w.assertNoMerge()
	if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
		t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
	}
	if len(w.prLabels) != 0 {
		t.Errorf("pull request labels = %v, want none without a pull request", w.prLabels)
	}
	if got := w.calls("gh", "pr", "view"); len(got) != 0 {
		t.Errorf("read a pull request that the refused push never updated: %v", got)
	}
	w.assertSandboxesKept()
	w.assertEventOnce(`^#42 ready for human: no pull request: .*\.github/workflows/ci\.yaml, \.github/workflows/release\.yml.*ADR-0003`)
	if summary := w.stdout.String(); !strings.Contains(summary, "needs a person: #42") ||
		!strings.Contains(summary, ".github/workflows/release.yml") || !strings.Contains(summary, "ADR-0003") {
		t.Errorf("summary does not show the ticket as ready for a person:\n%s", summary)
	}
}

func TestWorkflowChangeWinsOverWhatTheImplementerReports(t *testing.T) {
	cases := map[string]string{
		"blocked on the refused push": `{"status":"blocked","pr":0,"reason":"GitHub refused the push for the workflow file"}`,
		"finished anyway":             finishedResult,
	}
	for name, output := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.implementerOutput = output
			w.implementerFiles = [][]string{{".github/workflows/release.yml"}}

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
			}
			if w.implementerRuns != 1 {
				t.Errorf("implementer passes = %d, want 1", w.implementerRuns)
			}
			w.assertNoMerge()
			w.assertEventOnce(`^#42 ready for human: .*\.github/workflows/release\.yml.*ADR-0003`)
			if strings.Contains(w.stdout.String(), "the Implementer is blocked") {
				t.Errorf("used the Implementer's answer instead of the work:\n%s", w.stdout.String())
			}
		})
	}
}

func TestWorkflowChangeAfterAHandBackStopsThere(t *testing.T) {
	w := newWorld(t)
	w.reviewerOutput = rejectVerdict
	w.implementerFiles = [][]string{{"main.go"}, {".github/workflows/release.yml"}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if w.implementerRuns != 2 {
		t.Errorf("implementer passes = %d, want 2", w.implementerRuns)
	}
	if w.reviewerRuns != 1 {
		t.Errorf("reviewers = %d, want none on the workflow change", w.reviewerRuns)
	}
	w.assertNoMerge()
	if !slices.Equal(w.prLabels, []string{"ready-for-human"}) {
		t.Errorf("pull request labels = %v, want ready-for-human", w.prLabels)
	}
	w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: .*\.github/workflows/release\.yml.*ADR-0003`)
}

func TestOtherGithubFilesAreTreatedAsBefore(t *testing.T) {
	w := newWorld(t)
	w.implementerFiles = [][]string{{
		".github/CODEOWNERS",
		".github/ISSUE_TEMPLATE/bug.md",
		".github/workflows-old/ci.yml",
	}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if w.reviewerRuns != 1 {
		t.Errorf("reviewers = %d, want the work treated as before", w.reviewerRuns)
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want 1", got)
	}
	if slices.Contains(w.ticketLabels, "ready-for-human") {
		t.Errorf("ticket labels = %v, want no person for other .github files", w.ticketLabels)
	}
}

func TestWorkflowChangeDoesNotStopTheRunAtItsOtherTickets(t *testing.T) {
	w := newWorld(t)
	w.addTicket(43, w.ticketOpened.Add(time.Hour))
	w.implementerFiles = [][]string{{".github/workflows/release.yml"}}

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d, want the run to carry on\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got := len(w.calls("gh", "pr", "merge", "143")); got != 1 {
		t.Errorf("merges of #43's pull request = %d, want the run to carry on", got)
	}
	summary := w.stdout.String()
	if !strings.Contains(summary, "needs a person: #42") || !strings.Contains(summary, "merged: #43") {
		t.Errorf("summary does not show #42 for a person and #43 merged:\n%s", summary)
	}
}
