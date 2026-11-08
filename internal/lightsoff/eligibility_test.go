package lightsoff_test

import (
	"cmp"
	"slices"
	"strings"
	"testing"
)

func TestLabelAppliedBySomeoneOffTheAllowlistFailsTheLabelCheck(t *testing.T) {
	w := newWorld(t)
	w.labelEvents = []labelEvent{{"labeled", "ready-for-agent", "stranger"}}

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	w.assertStderrHas("stranger", "not on the allowlist")
}

func TestOverrideStartsTheTicketOnlyWhenThePersonTypesYes(t *testing.T) {
	cases := map[string]bool{
		"y\n":     true,
		"yes\n":   true,
		" YES \n": true,
		"Y":       true,
		"no\n":    false,
		"\n":      false,
		"yess\n":  false,
		"":        false, // stdin closed with nothing typed
	}
	for input, starts := range cases {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			w := newWorld(t)
			w.ticketLabels = nil
			w.terminal = true
			w.input = input

			code := w.run("owner/repo", "--ticket=42")
			w.assertStderrHas("no ready-for-agent label", "[y/N]")
			if !starts {
				w.assertNotStarted(code)
				return
			}
			if code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
			if len(w.calls("sbx", "create")) == 0 {
				t.Errorf("confirmed ticket did not start")
			}
		})
	}
}

func TestTicketThatPassesEveryCheckStartsWithoutAsking(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if strings.Contains(w.stderr.String(), "[y/N]") {
		t.Errorf("asked to confirm a ticket that passes:\n%s", w.stderr.String())
	}
}

func TestFailedChecksWithNoTerminalAreRefused(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = nil
	w.input = "yes\n" // piped, as cron or a script might

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	w.assertStderrHas("no ready-for-agent label", "no terminal")
}

func TestClosedTicketIsRefusedEvenWithConfirmation(t *testing.T) {
	w := newWorld(t)
	w.ticketState = "CLOSED"
	w.confirm()

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	w.assertStderrHas("#42 is closed")
	if strings.Contains(w.stderr.String(), "[y/N]") {
		t.Errorf("offered to override a closed ticket:\n%s", w.stderr.String())
	}
}

func TestClaimedTicketListsTheClaimAsAFailedCheck(t *testing.T) {
	w := newWorld(t)
	w.ticketLabels = []string{"agent-in-progress"}

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	w.assertStderrHas("no ready-for-agent label", "already claimed")
}

func TestLabelCheckFollowsWhoLastAppliedTheLabel(t *testing.T) {
	cases := map[string]struct {
		events []labelEvent
		passes bool
	}{
		"reapplied by the allowlist after a stranger": {
			events: []labelEvent{
				{"labeled", "ready-for-agent", "stranger"},
				{"unlabeled", "ready-for-agent", "owner"},
				{"labeled", "ready-for-agent", "owner"},
			},
			passes: true,
		},
		"reapplied by a stranger after the allowlist": {
			events: []labelEvent{
				{"labeled", "ready-for-agent", "owner"},
				{"unlabeled", "ready-for-agent", "stranger"},
				{"labeled", "ready-for-agent", "stranger"},
			},
		},
		"allowlisted login in another case": {
			events: []labelEvent{{"labeled", "Ready-For-Agent", "OWNER"}},
			passes: true,
		},
		"another label applied by the allowlist": {
			events: []labelEvent{
				{"labeled", "ready-for-agent", "stranger"},
				{"labeled", "bug", "owner"},
			},
		},
		"no record of the label": {},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.labelEvents = c.events

			code := w.run("owner/repo", "--ticket=42")
			if !c.passes {
				w.assertNotStarted(code)
				w.assertStderrHas("ready-for-agent")
				return
			}
			if code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
		})
	}
}

func TestUnreadableLabelHistoryIsAToolFailure(t *testing.T) {
	w := newWorld(t)
	w.eventsExit = 1
	w.confirm()

	w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
	w.assertStderrHas("HTTP 502")
	if strings.Contains(w.stderr.String(), "[y/N]") {
		t.Errorf("a gh failure was offered as an override:\n%s", w.stderr.String())
	}
}

func TestClaimingSwapsReadyForAgentForAgentInProgressBeforeWorkStarts(t *testing.T) {
	cases := map[string]struct {
		onTicket, inRepo []string
		confirm          bool
	}{
		"free ticket": {
			onTicket: []string{"ready-for-agent", "bug"},
			inRepo:   []string{"ready-for-agent", "agent-in-progress", "bug"},
		},
		"repository has no claim label yet": {
			onTicket: []string{"ready-for-agent"},
			inRepo:   []string{"ready-for-agent"},
		},
		"overridden ticket with no labels": {
			inRepo:  []string{"ready-for-agent", "agent-in-progress"},
			confirm: true,
		},
		"claim left by a crash": {
			onTicket: []string{"agent-in-progress"},
			inRepo:   []string{"ready-for-agent", "agent-in-progress"},
			confirm:  true,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.ticketLabels, w.repoLabels = c.onTicket, c.inRepo
			if c.confirm {
				w.confirm()
			}

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
			}
			if !slices.Contains(w.ticketLabels, "agent-in-progress") || slices.Contains(w.ticketLabels, "ready-for-agent") {
				t.Errorf("ticket labels = %v, want agent-in-progress in place of ready-for-agent", w.ticketLabels)
			}
			if slices.Contains(c.onTicket, "bug") && !slices.Contains(w.ticketLabels, "bug") {
				t.Errorf("ticket labels = %v, lost an unrelated label", w.ticketLabels)
			}
			claim := w.indexOf("gh", "issue", "edit", "agent-in-progress")
			if claim < 0 || claim > w.indexOf("sbx", "create") {
				t.Errorf("ticket not claimed before its sandbox was created: %v", w.commands)
			}
			w.assertEventOnce(`^#42 claimed`)
		})
	}
}

func TestClaimThatFailsIsAToolFailureBeforeWorkStarts(t *testing.T) {
	w := newWorld(t)
	w.labelEditExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created for an unclaimed ticket: %v", got)
	}
}

func TestOpenBlockersFailTheCheckWhicheverWayTheyAreRecorded(t *testing.T) {
	cases := map[string]struct {
		body     string
		native   []string
		states   map[string]string
		wantOpen []string // named as open blockers; none means the ticket is free
	}{
		"native link to an open ticket": {
			native:   []string{"owner/repo#3"},
			states:   map[string]string{"owner/repo#3": "OPEN"},
			wantOpen: []string{"#3"},
		},
		"native link to a closed ticket": {
			native: []string{"owner/repo#3"},
			states: map[string]string{"owner/repo#3": "CLOSED"},
		},
		"native link to another repository": {
			native:   []string{"other/lib#5"},
			states:   map[string]string{"other/lib#5": "OPEN"},
			wantOpen: []string{"other/lib#5"},
		},
		"to-tickets section": {
			body:     "## What to build\n\nSum.\n\n## Blocked by\n\n- #3 Parser\n- #4\n\n## Notes\n\nSee #9.\n",
			states:   map[string]string{"owner/repo#3": "OPEN", "owner/repo#4": "CLOSED", "owner/repo#9": "OPEN"},
			wantOpen: []string{"#3"},
		},
		"inline line": {
			body:     "Blocked by: #3, #4\n\nSum.\n",
			states:   map[string]string{"owner/repo#3": "CLOSED", "owner/repo#4": "OPEN"},
			wantOpen: []string{"#4"},
		},
		"bold inline line": {
			body:     "**Blocked by:** other/lib#5\n",
			states:   map[string]string{"other/lib#5": "OPEN"},
			wantOpen: []string{"other/lib#5"},
		},
		"link to the ticket": {
			body:     "## Blocked by\n\n- https://github.com/owner/repo/issues/3\n",
			states:   map[string]string{"owner/repo#3": "OPEN"},
			wantOpen: []string{"#3"},
		},
		"none": {
			body: "## Blocked by\n\nNone (can start immediately)\n",
		},
		"other ways of saying none": {
			body: "## Blocked by\n\nN/A\n\n**Blocked by:** —\n\nBlocked by: nothing\n",
		},
		"bare line before other fields": {
			body:     "# 03: Sum\n\n**Blocked by:**\n- #3\n\n**Status:** ready-for-agent\n\n- [ ] Adds numbers\n",
			states:   map[string]string{"owner/repo#3": "OPEN"},
			wantOpen: []string{"#3"},
		},
		"link with a fragment": {
			body:     "## Blocked by\n\n- #3, see https://example.com/notes#12\n",
			states:   map[string]string{"owner/repo#3": "OPEN"},
			wantOpen: []string{"#3"},
		},
		"link to a pull request": {
			body:     "## Blocked by\n\n- https://github.com/owner/repo/pull/5\n",
			states:   map[string]string{"owner/repo#5": "OPEN"},
			wantOpen: []string{"#5"},
		},
		"all closed in both": {
			body:   "## Blocked by\n\n- #3\n",
			native: []string{"owner/repo#4"},
			states: map[string]string{"owner/repo#3": "CLOSED", "owner/repo#4": "CLOSED"},
		},
		"open in both": {
			body:     "## Blocked by\n\n- #3\n",
			native:   []string{"owner/repo#3", "owner/repo#4"},
			states:   map[string]string{"owner/repo#3": "OPEN", "owner/repo#4": "OPEN"},
			wantOpen: []string{"#3", "#4"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.ticketBody = cmp.Or(c.body, w.ticketBody)
			w.blockedBy = c.native
			w.issueStates = c.states

			code := w.run("owner/repo", "--ticket=42")
			if len(c.wantOpen) == 0 {
				if code != 0 {
					t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
				}
				return
			}
			w.assertNotStarted(code)
			w.assertStderrHas("it has open blockers: " + strings.Join(c.wantOpen, ", ") + "\n")
			if strings.Contains(w.stderr.String(), "can't be resolved") {
				t.Errorf("a blocker was unresolvable:\n%s", w.stderr.String())
			}
		})
	}
}

func TestBlockersThatCannotBeResolvedAreNotTreatedAsFree(t *testing.T) {
	cases := map[string]struct {
		body        string
		nativeFails bool
		notice      string
	}{
		"ticket that doesn't exist": {
			body:   "## Blocked by\n\n- #99\n",
			notice: "#99",
		},
		"blocker named by its title": {
			body:   "## Blocked by\n\n- The parser ticket\n",
			notice: "The parser ticket",
		},
		"native links unreadable": {
			nativeFails: true,
			notice:      "HTTP 404",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.ticketBody = cmp.Or(c.body, w.ticketBody)
			if c.nativeFails {
				w.blockedExit = 1
			}

			w.assertNotStarted(w.run("owner/repo", "--ticket=42"))
			w.assertStderrHas("can't be resolved", c.notice)
		})
	}
}

func TestOverriddenOpenBlockerStillStartsWhenConfirmed(t *testing.T) {
	w := newWorld(t)
	w.blockedBy = []string{"owner/repo#3"}
	w.issueStates = map[string]string{"owner/repo#3": "OPEN"}
	w.confirm()

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	w.assertStderrHas("open blockers: #3\n")
}
