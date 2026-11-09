package lightsoff_test

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	secondHead    = "cccc3333"
	rejectVerdict = `{"mergeable":false,"risk":"low","update":"two-way","reason":"sum subtracts"}`
)

func TestRequestedChangesGoBackToTheImplementerWhoFixesThemAndMerges(t *testing.T) {
	w := newWorld(t)
	w.reviewerPasses = []agentPass{{output: rejectVerdict}}
	w.heads = []string{headSHA, secondHead}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}

	if got := len(w.calls("sbx", "create")); got != 3 {
		t.Fatalf("sandboxes = %v, want one Implementer and two Reviewers", w.sandboxes)
	}
	implementer := w.sandboxes[0]
	prompts := w.prompts(implementer)
	if len(prompts) != 2 {
		t.Fatalf("implementer passes in its sandbox = %d, want 2", len(prompts))
	}
	for _, want := range []string{"sum subtracts", "Make sum add numbers.", "#7", "Do not merge", "Do not run the code-review skill", `"status"`} {
		if !strings.Contains(prompts[1], want) {
			t.Errorf("second implementer prompt lacks %q:\n%s", want, prompts[1])
		}
	}
	if strings.Contains(prompts[0], "sum subtracts") {
		t.Errorf("first prompt holds a reason that didn't exist yet:\n%s", prompts[0])
	}

	merges := w.calls("gh", "pr", "merge")
	if len(merges) != 1 {
		t.Fatalf("merges = %v, want one", merges)
	}
	if flagValue(merges[0].Args, "--match-head-commit") != secondHead {
		t.Errorf("merge not pinned to the head the second Reviewer judged: %v", merges[0].Args)
	}
	if w.indexOf("git", "checkout", secondHead) > w.indexOf("sbx", "create", w.sandboxes[2]) {
		t.Errorf("second Reviewer must start on the new head")
	}
	if slices.Contains(w.ticketLabels, "ready-for-human") {
		t.Errorf("ticket labels = %v, a fixed ticket needs no person", w.ticketLabels)
	}
	w.assertEventOnce(`^#42 handed back after iteration 1 of 3: .*sum subtracts`)
	w.assertEventOnce(`^#42 implementer resumed in sandbox ` + implementer)
	w.assertEventOnce(`^#42 merged pull request #7`)
}

func TestThreeUsedIterationsStopAtAPersonWithTheSandboxesKept(t *testing.T) {
	w := newWorld(t)
	w.reviewerOutput = rejectVerdict

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
	}

	if got := len(w.prompts(w.sandboxes[0])); got != 3 {
		t.Errorf("implementer passes = %d, want 3", got)
	}
	if len(w.sandboxes) != 4 {
		t.Errorf("sandboxes = %v, want one Implementer and three Reviewers", w.sandboxes)
	}
	w.assertNoMerge()
	w.assertSandboxesKept()
	if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
		t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
	}
	if !slices.Equal(w.prLabels, []string{"ready-for-human"}) {
		t.Errorf("pull request labels = %v, want ready-for-human", w.prLabels)
	}
	if got := w.calls("gh", "pr", "comment", "7"); len(got) != 3 {
		t.Errorf("verdicts posted = %d, want one per Reviewer", len(got))
	}
	w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: 3 iterations used.*sum subtracts`)
}

func TestImplementerIsStoppedDuringReviewAndNothingElseRunsBesideAnAgent(t *testing.T) {
	w := newWorld(t)
	w.reviewerOutput = rejectVerdict

	w.run("owner/repo", "--ticket=42")

	implementer := w.sandboxes[0]
	stop, review := w.indexOf("sbx", "stop", implementer), w.indexOf("sbx", "exec", w.sandboxes[1], "dsh")
	if stop < 0 || stop > review {
		t.Errorf("implementer stopped at %d, reviewer ran at %d", stop, review)
	}
	if len(w.overlaps) != 0 {
		t.Errorf("running beside an agent: %v", w.overlaps)
	}
	for i, name := range w.sandboxes {
		for _, other := range w.sandboxes[i+1:] {
			if name == other {
				t.Errorf("sandbox %q used twice: %v", name, w.sandboxes)
			}
		}
	}
}

func TestSandboxThatCannotBeStoppedIsAToolFailure(t *testing.T) {
	w := newWorld(t)
	w.stopExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	w.assertNoMerge()
	w.assertSandboxesKept()
	if got := len(w.prompts(w.sandboxes[0])); got != 1 {
		t.Errorf("implementer passes = %d, want a tool failure not to spend another", got)
	}
}

func TestACrashUsesAnIterationAndTheNextPassIsToldWhy(t *testing.T) {
	cases := map[string]struct {
		pass agentPass
		want string
	}{
		"non-zero exit":  {agentPass{exit: 1, stderr: "thinking\nmodel error"}, "exited 1: model error"},
		"out of memory":  {agentPass{exit: 137}, "out of memory"},
		"past the limit": {agentPass{hangs: true}, "60-minute"},
		"no JSON":        {agentPass{output: "All done!"}, "no JSON object"},
		"unknown status": {agentPass{output: `{"status":"done","pr":7,"reason":"x"}`}, "unknown implementer status"},
		"no number":      {agentPass{output: `{"status":"finished"}`}, "names no pull request"},
	}
	for name, c := range cases {
		t.Run("implementer: "+name, func(t *testing.T) {
			w := newWorld(t)
			w.implementerPasses = []agentPass{c.pass}

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
			}
			prompts := w.prompts(w.sandboxes[0])
			if len(prompts) != 2 {
				t.Fatalf("implementer passes = %d, want 2", len(prompts))
			}
			for _, want := range []string{"crashed", c.want} {
				if !strings.Contains(prompts[1], want) {
					t.Errorf("next pass not told %q:\n%s", want, prompts[1])
				}
			}
			// The crash may have come before it branched or opened anything.
			for _, want := range []string{"Closes #42", "Create a new branch"} {
				if !strings.Contains(prompts[1], want) {
					t.Errorf("next pass lacks %q:\n%s", want, prompts[1])
				}
			}
			if got := len(w.calls("gh", "pr", "merge")); got != 1 {
				t.Errorf("merges = %d, want the second pass to carry on to a merge", got)
			}
		})
	}

}

func TestACrashedReviewerIsReplacedOnTheSameCommitWithoutAnIteration(t *testing.T) {
	cases := map[string]struct {
		pass agentPass
		want string
	}{
		"non-zero exit":  {agentPass{output: mergeVerdict, exit: 1, stderr: "boom"}, "exited 1: boom"},
		"out of memory":  {agentPass{output: mergeVerdict, exit: 137}, "out of memory"},
		"past the limit": {agentPass{output: mergeVerdict, hangs: true}, "60-minute"},
		"no JSON":        {agentPass{output: "Ship it."}, "no JSON object"},
		"invalid":        {agentPass{output: `{"mergeable":"true","risk":"low","update":"two-way","reason":"x"}`}, "invalid verdict"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.reviewerPasses = []agentPass{c.pass}

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
			}
			if got := len(w.prompts(w.sandboxes[0])); got != 1 {
				t.Errorf("implementer passes = %d, want 1: it has nothing to fix", got)
			}
			if len(w.sandboxes) != 3 {
				t.Fatalf("sandboxes = %v, want a fresh Reviewer after the crash", w.sandboxes)
			}
			if w.clonedFrom[w.sandboxes[2]] != headSHA {
				t.Errorf("second Reviewer cloned %q, want the same commit %q", w.clonedFrom[w.sandboxes[2]], headSHA)
			}
			if !strings.Contains(w.sandboxes[2], "-reviewer-2-") {
				t.Errorf("second Reviewer's sandbox = %q, want it numbered 2", w.sandboxes[2])
			}
			if len(w.overlaps) != 0 {
				t.Errorf("running beside an agent: %v", w.overlaps)
			}
			if got := w.calls("gh", "pr", "comment", "7"); len(got) != 1 {
				t.Errorf("verdicts posted = %d, want only the one that parsed", len(got))
			}
			if got := len(w.calls("gh", "pr", "merge")); got != 1 {
				t.Errorf("merges = %d, want the second Reviewer's verdict to merge", got)
			}
			w.assertEventOnce(`^#42 reviewer crashed, so a fresh one reviews the same commit: .*` + regexp.QuoteMeta(c.want))
			if strings.Contains(w.stdout.String(), "handed back") {
				t.Errorf("a Reviewer crash handed back:\n%s", w.stdout.String())
			}
		})
	}
}

func TestAReviewerThatCrashesAgainStopsAtAPerson(t *testing.T) {
	w := newWorld(t)
	w.reviewerPasses = []agentPass{{exit: 137}, {output: "Ship it."}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.prompts(w.sandboxes[0])); got != 1 {
		t.Errorf("implementer passes = %d, want 1", got)
	}
	if got := len(w.calls("sbx", "create")); got != 3 {
		t.Errorf("sandboxes = %v, want one Implementer and two Reviewers", w.sandboxes)
	}
	w.assertNoMerge()
	w.assertSandboxesKept()
	w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: 2 Reviewers crashed on the same commit, the last one: .*no JSON object`)
}

func TestAgentsRunUnderTheSixtyMinuteLimitAndOnlyAgents(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	for _, c := range w.commands {
		want := time.Duration(0)
		if c.Name == "sbx" && slices.Contains(c.Args, "dsh") {
			want = 60 * time.Minute
		}
		if c.Timeout != want {
			t.Errorf("%s %s has limit %v, want %v", c.Name, c.Args[0], c.Timeout, want)
		}
	}
}

func TestAgentPastTheLimitIsStoppedBeforeAnythingElseHappens(t *testing.T) {
	t.Run("implementer", func(t *testing.T) {
		w := newWorld(t)
		w.implementerPasses = []agentPass{{hangs: true}}

		w.run("owner/repo", "--ticket=42")

		// Killing sbx exec on the host can leave the agent running in the sandbox.
		stop, next := w.indexOf("sbx", "stop", w.sandboxes[0]), w.indexOf("gh", "pr", "view")
		if stop < 0 || stop > next {
			t.Errorf("hung implementer stopped at %d, next step at %d", stop, next)
		}
	})
	t.Run("reviewer", func(t *testing.T) {
		w := newWorld(t)
		w.reviewerPasses = []agentPass{{hangs: true}}

		w.run("owner/repo", "--ticket=42")

		if w.indexOf("sbx", "stop", w.sandboxes[1]) < 0 {
			t.Errorf("hung reviewer not stopped")
		}
	})
}

func TestSbxFailingUnderAnAgentIsTriedAgainAfterAPause(t *testing.T) {
	w := newWorld(t)
	w.implementerPasses = []agentPass{{exit: 125, stderr: "503 Service Unavailable: the host is not fully awake"}}
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.prompts(w.sandboxes[0])); got != 2 {
		t.Errorf("implementer runs = %d, want the failed one tried again", got)
	}
	if w.now.Sub(start) < 30*time.Second {
		t.Errorf("tried again after %v, want a pause first", w.now.Sub(start))
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want the run to carry on", got)
	}
}

func TestAFailedSandboxCreateIsCleanedUpAndTriedAgain(t *testing.T) {
	w := newWorld(t)
	w.createFails = 2

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	creates := w.calls("sbx", "create")
	if len(creates) != 4 {
		t.Fatalf("creates = %d, want two failed and then one per agent", len(creates))
	}
	name := flagValue(creates[0].Args, "--name")
	if flagValue(creates[2].Args, "--name") != name {
		t.Errorf("retried create used a new name: %v", creates[2].Args)
	}
	removed := 0
	for _, c := range w.commands {
		if c.Name == "sbx" && c.Args[0] == "rm" && slices.Contains(c.Args, name) {
			removed++
		}
		if c.Name == "sbx" && c.Args[0] == "exec" {
			break // the create that worked came before the first agent ran
		}
	}
	if removed != 2 {
		t.Errorf("removals of %s before it worked = %d, want one after each failed create", name, removed)
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want the run to carry on", got)
	}
}

func TestAFailedSandboxCreateEveryTimeStopsTheTicket(t *testing.T) {
	w := newWorld(t)
	w.createFails = 3

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := len(w.calls("sbx", "create")); got != 3 {
		t.Errorf("creates = %d, want three tries", got)
	}
	w.assertEventOnce(`^#42 stopped: sbx create exited 1: error: failed to apply kit to sandbox`)
}

func TestSbxFailingUnderAnAgentEveryTimeIsAToolFailureNotACrash(t *testing.T) {
	w := newWorld(t)
	failing := agentPass{exit: 125, stderr: "sbx: daemon not running"}
	w.implementerPasses = []agentPass{failing, failing, failing}

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := len(w.prompts(w.sandboxes[0])); got != 3 {
		t.Errorf("implementer runs = %d, want three tries and no Iteration spent on them", got)
	}
	w.assertNotMovedToReadyForHuman()
	w.assertSandboxesKept()
}

func TestRepeatedCrashesStopAtAPersonWithoutAPullRequest(t *testing.T) {
	w := newWorld(t)
	w.implementerExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
	}
	if got := len(w.prompts(w.sandboxes[0])); got != 3 {
		t.Errorf("implementer passes = %d, want 3", got)
	}
	w.assertNoMerge()
	w.assertSandboxesKept()
	if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
		t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
	}
	if got := w.calls("gh", "pr", "edit"); len(got) != 0 {
		t.Errorf("labelled a pull request that was never seen: %v", got)
	}
	w.assertEventOnce(`^#42 ready for human: no pull request: 3 iterations used`)
}

func TestPullRequestThatDoesNotCheckOutGoesBackWithTheReason(t *testing.T) {
	w := newWorld(t)
	w.prBody = "Relates to #42"

	w.run("owner/repo", "--ticket=42")

	prompts := w.prompts(w.sandboxes[0])
	if len(prompts) < 2 || !strings.Contains(prompts[1], `does not contain "Closes #42"`) {
		t.Errorf("next pass not told what's wrong with the pull request: %q", prompts)
	}
}

func TestMergeConflictUsesAnIterationBeforeCIOrReview(t *testing.T) {
	w := newWorld(t)
	w.conflictedPasses = 1
	w.checks = []string{pendingChecks, greenChecks} // CI doesn't finish on a conflicted pull request
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if len(w.sandboxes) != 2 {
		t.Errorf("sandboxes = %v, want no Reviewer on the conflicted pull request", w.sandboxes)
	}
	prompts := w.prompts(w.sandboxes[0])
	if len(prompts) != 2 || !strings.Contains(prompts[1], "merge conflict with main") {
		t.Fatalf("next pass not told about the conflict: %q", prompts)
	}
	if got := w.now.Sub(start); got > 5*time.Minute {
		t.Errorf("waited %v on a pull request that can't get CI", got)
	}
	if got := len(w.calls("gh", "pr", "merge")); got != 1 {
		t.Errorf("merges = %d, want one once the conflict is fixed", got)
	}
}

func TestMergeConflictsThroughEveryIterationStopAtAPersonWithThePullRequest(t *testing.T) {
	w := newWorld(t)
	w.conflictedPasses = 3

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	w.assertNoMerge()
	if w.implementerRuns != 3 {
		t.Errorf("implementer passes = %d, want 3", w.implementerRuns)
	}
	if !slices.Equal(w.prLabels, []string{"ready-for-human"}) {
		t.Errorf("pull request labels = %v, want ready-for-human", w.prLabels)
	}
	w.assertSandboxesKept()
	w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: 3 iterations used.*merge conflict`)
}

func TestConflictFoundAtTheMergeGoesBackAndTheFixIsReviewedAgain(t *testing.T) {
	w := newWorld(t)
	w.conflictAtMerge = true
	w.heads = []string{headSHA, secondHead}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	merges := w.calls("gh", "pr", "merge")
	if len(merges) != 2 {
		t.Fatalf("merges = %v, want the failed one and the one after the fix", merges)
	}
	if flagValue(merges[1].Args, "--match-head-commit") != secondHead {
		t.Errorf("second merge not pinned to the new head: %v", merges[1].Args)
	}
	if w.reviewerRuns != 2 {
		t.Errorf("reviewers = %d, want the fix judged by a new one", w.reviewerRuns)
	}
	if prompts := w.prompts(w.sandboxes[0]); !strings.Contains(prompts[1], "merge conflict with main") {
		t.Errorf("next pass not told about the conflict:\n%s", prompts[1])
	}
	w.assertEventOnce(`^#42 merged pull request #7`)
}

func TestAnEarlierVerdictNeverMergesALaterHead(t *testing.T) {
	w := newWorld(t)
	w.conflictAtMerge = true
	w.heads = []string{headSHA, secondHead}
	w.reviewerPasses = []agentPass{{output: mergeVerdict}, {output: "Still fine, ship it."}, {exit: 1, output: mergeVerdict}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	merges := w.calls("gh", "pr", "merge")
	if len(merges) != 1 || flagValue(merges[0].Args, "--match-head-commit") != headSHA {
		t.Errorf("merges = %v, want only the failed one, on the head its Verdict judged", merges)
	}
	w.assertEventOnce(`^#42 ready for human`)
}

func TestBlockedImplementerStopsAtAPersonWithoutUsingAnotherIteration(t *testing.T) {
	blocked := `{"status":"blocked","pr":0,"reason":"the ticket contradicts itself"}`

	t.Run("before any pull request", func(t *testing.T) {
		w := newWorld(t)
		w.implementerOutput = blocked

		if code := w.run("owner/repo", "--ticket=42"); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if w.implementerRuns != 1 {
			t.Errorf("implementer passes = %d, want 1", w.implementerRuns)
		}
		w.assertNoMerge()
		w.assertSandboxesKept()
		if !slices.Equal(w.ticketLabels, []string{"ready-for-human"}) {
			t.Errorf("ticket labels = %v, want only ready-for-human", w.ticketLabels)
		}
		w.assertEventOnce(`^#42 ready for human: no pull request: .*the ticket contradicts itself`)
	})

	t.Run("after a hand-back, with a pull request open", func(t *testing.T) {
		w := newWorld(t)
		w.implementerPasses = []agentPass{{output: finishedResult}, {output: blocked}}
		w.reviewerOutput = rejectVerdict

		if code := w.run("owner/repo", "--ticket=42"); code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
		if w.implementerRuns != 2 {
			t.Errorf("implementer passes = %d, want 2", w.implementerRuns)
		}
		w.assertNoMerge()
		if !slices.Equal(w.prLabels, []string{"ready-for-human"}) {
			t.Errorf("pull request labels = %v, want ready-for-human", w.prLabels)
		}
		w.assertEventOnce(`^#42 ready for human: pull request #7 stays open: .*the ticket contradicts itself`)
	})
}

func TestConflictGitHubIsStillWorkingOutGoesBackOnceItKnows(t *testing.T) {
	w := newWorld(t)
	w.conflictAtMerge = true
	w.unknownAfterMerge = 2

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if prompts := w.prompts(w.sandboxes[0]); len(prompts) != 2 || !strings.Contains(prompts[1], "merge conflict with main") {
		t.Errorf("next pass not told about the conflict: %q", prompts)
	}
}

func TestFailedMergeGitHubCanNotExplainIsAToolFailureAfterABoundedWait(t *testing.T) {
	w := newWorld(t)
	w.conflictAtMerge = true
	w.unknownAfterMerge = 1000
	start := w.now

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if waited := w.now.Sub(start); waited > 2*time.Minute {
		t.Errorf("waited %v", waited)
	}
	if w.implementerRuns != 1 {
		t.Errorf("implementer passes = %d, want no Iteration spent on it", w.implementerRuns)
	}
}

func TestImplementerKeepsToItsPullRequestAcrossIterations(t *testing.T) {
	w := newWorld(t)
	w.implementerPasses = []agentPass{
		{output: finishedResult},
		{output: `{"status":"finished","pr":8,"reason":"started again"}`},
		{output: finishedResult},
	}
	w.reviewerPasses = []agentPass{{output: rejectVerdict}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	prompts := w.prompts(w.sandboxes[0])
	if len(prompts) != 3 || !strings.Contains(prompts[2], "#8") || !strings.Contains(prompts[2], "#7") {
		t.Errorf("third pass not told to go back to its pull request: %q", prompts)
	}
	if got := w.calls("gh", "pr", "merge", "8"); len(got) != 0 {
		t.Errorf("merged the other pull request: %v", got)
	}
	if got := w.calls("gh", "pr", "merge", "7"); len(got) != 1 {
		t.Errorf("merges of #7 = %v, want one", got)
	}
}

func TestPullRequestClosedMidAttemptIsNotAskedForOrLabelled(t *testing.T) {
	w := newWorld(t)
	w.closedFromPass = 2
	w.reviewerOutput = rejectVerdict

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	prompts := w.prompts(w.sandboxes[0])
	if len(prompts) != 3 {
		t.Fatalf("implementer passes = %d, want 3", len(prompts))
	}
	if strings.Contains(prompts[2], "do not open another") || !strings.Contains(prompts[2], "Closes #42") {
		t.Errorf("third pass told to keep using a closed pull request:\n%s", prompts[2])
	}
	if got := w.calls("gh", "pr", "edit"); len(got) != 0 {
		t.Errorf("labelled a closed pull request: %v", got)
	}
	w.assertEventOnce(`^#42 ready for human: no pull request: 3 iterations used`)
}

func TestBlockedWithoutAReasonCountsAsACrash(t *testing.T) {
	w := newWorld(t)
	w.implementerPasses = []agentPass{{output: `{"status":"blocked","pr":0}`}}

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if w.implementerRuns != 2 {
		t.Errorf("implementer passes = %d, want it to use an Iteration", w.implementerRuns)
	}
}
