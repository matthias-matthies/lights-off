package lightsoff_test

import (
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRedCIGoesBackToTheImplementerWithoutAReviewer(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{redChecks, greenChecks} // red after the first pass, green after the fix
	w.heads = []string{headSHA, secondHead}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.calls("sbx", "create")); got != 2 {
		t.Fatalf("sandboxes = %v, want the Implementer's and one Reviewer, for the green head only", w.sandboxes)
	}
	if w.reviewerRuns != 1 {
		t.Errorf("reviewer runs = %d, want one, after CI went green", w.reviewerRuns)
	}
	prompts := w.prompts(w.sandboxes[0])
	if len(prompts) != 2 {
		t.Fatalf("implementer passes = %d, want 2", len(prompts))
	}
	for _, want := range []string{"CI failed", "test"} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("second implementer prompt lacks %q:\n%s", want, prompts[1])
		}
	}
	w.assertEventOnce(`^#42 handed back after iteration 1 of 3: CI failed on pull request #7`)
	w.assertEventOnce(`^#42 merged pull request #7`)
}

func TestCIUnfinishedAfterThirtyMinutesStopsAtAPerson(t *testing.T) {
	w := newWorld(t)
	w.checks = []string{pendingChecks}
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0: a stuck pipeline is an expected outcome\nstdout:\n%s", code, w.stdout.String())
	}
	w.assertNoMerge()
	if w.reviewerRuns != 0 || w.implementerRuns != 1 {
		t.Errorf("implementer passes = %d, reviewers = %d; want one pass and no Reviewer: another pass can't unstick CI",
			w.implementerRuns, w.reviewerRuns)
	}
	w.assertWaited(start, 30*time.Minute)
	if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
		t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
	}
	if !slices.Equal(w.prLabels, []string{"ready-for-human"}) {
		t.Errorf("pull request labels = %v, want ready-for-human", w.prLabels)
	}
	w.assertSandboxesKept()
	w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: CI not finished after 30m`)
}

func TestRepositoryWithoutCICountsAsGreenAfterTwoMinutes(t *testing.T) {
	w := newWorld(t)
	w.workflowFiles = nil
	w.checks = []string{`[]`}
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want one: no CI counts as green (ADR-0001)", got)
	}
	if w.reviewerRuns != 1 {
		t.Errorf("reviewer runs = %d, want one: the Verdict alone decides", w.reviewerRuns)
	}
	w.assertWaited(start, 2*time.Minute)
	w.assertEventOnce(`^#42 no CI: counted as green`)
}

func TestConfiguredCIWhoseChecksNeverAppearStopsAtAPersonAfterThirtyMinutes(t *testing.T) {
	cases := map[string]func(*world){
		"workflow file":              func(w *world) { w.workflowFiles = []string{"ci.yml"} },
		"workflow file as .yaml":     func(w *world) { w.workflowFiles = []string{"README.md", "test.yaml"} },
		"branch protection required": func(w *world) { w.workflowFiles, w.protectedChecks = nil, []string{"buildkite"} },
		"ruleset required":           func(w *world) { w.workflowFiles, w.rulesetChecks = nil, []string{"circleci"} },
	}
	for name, configure := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			configure(w)
			w.checks = []string{`[]`}
			start := w.now

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
			}
			w.assertNoMerge()
			if w.reviewerRuns != 0 {
				t.Errorf("reviewer runs = %d, want none", w.reviewerRuns)
			}
			w.assertWaited(start, 30*time.Minute)
			if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
				t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
			}
			w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: CI not finished after 30m`)
		})
	}
}

func TestWorkflowDirectoryWithoutWorkflowsIsNoCI(t *testing.T) {
	w := newWorld(t)
	w.workflowFiles = []string{"README.md"}
	w.checks = []string{`[]`}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want one: GitHub Actions runs only .yml and .yaml files", got)
	}
}

func TestChecksThatAppearDuringTheNoCIWaitAreWaitedFor(t *testing.T) {
	late := []string{`[]`, `[]`, pendingChecks, pendingChecks, pendingChecks, pendingChecks}

	t.Run("going green", func(t *testing.T) {
		w := newWorld(t)
		w.workflowFiles = nil // CI on an external service with no required check
		w.checks = append(slices.Clone(late), greenChecks)

		if code := w.run("owner/repo", "--ticket=42"); code != 0 {
			t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
		}
		if got := len(w.calls("gh", "pr", "merge")); got != 1 {
			t.Errorf("merges = %d, want one", got)
		}
		w.assertEventOnce(`^#42 CI green`)
		if strings.Contains(w.stdout.String(), "no CI") {
			t.Errorf("checks that appeared were skipped as no CI:\n%s", w.stdout.String())
		}
	})

	t.Run("going red", func(t *testing.T) {
		w := newWorld(t)
		w.workflowFiles = nil
		w.checks = append(slices.Clone(late), redChecks)

		w.run("owner/repo", "--ticket=42")

		if w.reviewerRuns != 0 {
			t.Errorf("reviewer runs = %d, want none on red CI", w.reviewerRuns)
		}
		w.assertNoMerge()
		w.assertEventOnce(`^#42 handed back after iteration 1 of 3: CI failed`)
	})
}

func TestNoCILookupThatFailsIsAToolFailureNotNoCI(t *testing.T) {
	w := newWorld(t)
	w.workflowFiles = nil
	w.workflowsExit = 1
	w.checks = []string{`[]`}

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	w.assertNoMerge()
	if w.reviewerRuns != 0 {
		t.Errorf("reviewer runs = %d, want none", w.reviewerRuns)
	}
	w.assertSandboxesKept()
	w.assertEventOnce(`^#42 stopped: .*HTTP 403`)
}

func TestBranchThatHidesItsProtectionIsAToolFailureNotNoCI(t *testing.T) {
	w := newWorld(t)
	w.workflowFiles = nil
	w.protectionHidden = true
	w.checks = []string{`[]`}

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	w.assertNoMerge()
	if w.reviewerRuns != 0 {
		t.Errorf("reviewer runs = %d, want none", w.reviewerRuns)
	}
}

func TestChecksThatAppearWhileTheRepositoryIsAskedAreWaitedFor(t *testing.T) {
	w := newWorld(t)
	w.workflowFiles = nil
	// Zero checks on the first read and every poll for two minutes; they appear
	// only on the read after the configuration lookup.
	w.checks = []string{`[]`, `[]`, `[]`, `[]`, `[]`, redChecks}

	w.run("owner/repo", "--ticket=42")

	if w.reviewerRuns != 0 {
		t.Errorf("reviewer runs = %d, want none on red CI", w.reviewerRuns)
	}
	w.assertNoMerge()
	if strings.Contains(w.stdout.String(), "no CI") {
		t.Errorf("checks that appeared were skipped as no CI:\n%s", w.stdout.String())
	}
}

func TestRulesetsThePlanDoesNotOfferAreNone(t *testing.T) {
	t.Run("no workflows: no CI", func(t *testing.T) {
		w := newWorld(t)
		w.workflowFiles = nil
		w.rulesExit = "plan"
		w.checks = []string{`[]`}

		if code := w.run("owner/repo", "--ticket=42"); code != 0 {
			t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
		}
		if got := len(w.calls("gh", "pr", "merge")); got != 1 {
			t.Errorf("merges = %d, want one", got)
		}
	})

	t.Run("workflows: configured", func(t *testing.T) {
		w := newWorld(t)
		w.rulesExit = "plan"
		w.checks = []string{`[]`}
		start := w.now

		if code := w.run("owner/repo", "--ticket=42"); code != 0 {
			t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
		}
		w.assertNoMerge()
		w.assertWaited(start, 30*time.Minute)
	})

	t.Run("any other refusal stops the run", func(t *testing.T) {
		w := newWorld(t)
		w.workflowFiles = nil
		w.rulesExit = "permission"
		w.checks = []string{`[]`}

		if code := w.run("owner/repo", "--ticket=42"); code == 0 {
			t.Errorf("exit code = 0, want non-zero")
		}
		w.assertNoMerge()
	})
}
