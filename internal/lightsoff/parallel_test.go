package lightsoff_test

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

// addIndependentTickets adds tickets 43 onwards until there are count in all,
// none blocking another.
func (w *world) addIndependentTickets(count int) {
	w.ticketOpened = day(0)
	for i := 1; i < count; i++ {
		w.addTicket(42+i, day(i))
	}
}

func TestTicketsAtOnceFollowHostMemory(t *testing.T) {
	cases := []struct {
		name   string
		memory uint64
		n      int
		config string
		want   int
	}{
		{"8 GB runs three", 8 << 30, 5, "", 3},
		{"16 GB runs five", 16 << 30, 5, "", 5},
		{"--n caps it", 64 << 30, 2, "", 2},
		{"too little memory still runs one", 2 << 30, 5, "", 1},
		{"an Implementer override counts", 16 << 30, 5, "implementer_memory = \"4g\"\n", 3},
		{"an override in megabytes", 8 << 30, 5, "implementer_memory = \"3072m\"\n", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.hostMemory = c.memory
			w.config += c.config
			w.addIndependentTickets(c.n)
			w.hold = c.want + 1 // more than lights-off should allow, so every pass waits out holdFor
			w.holdFor = 200 * time.Millisecond

			if code := w.run("owner/repo", fmt.Sprintf("--n=%d", c.n)); code != 0 {
				t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
			}
			w.assertEventOnce(fmt.Sprintf(`^lights-off: up to %d tickets? at once`, c.want))
			if w.mostAtOnce != c.want {
				t.Errorf("%d agents ran at once, want %d\nstdout:\n%s", w.mostAtOnce, c.want, w.stdout.String())
			}
			if got := len(w.claimed()); got != c.n {
				t.Errorf("claimed %d tickets, want %d", got, c.n)
			}
		})
	}
}

// Without --memory sbx gives each sandbox half of host memory, which a few
// tickets at once would overcommit.
func TestEverySandboxGetsAnExplicitMemoryLimit(t *testing.T) {
	w := newWorld(t)
	w.addIndependentTickets(3)
	w.run("owner/repo", "--n=3")

	p := newWorld(t)
	p.terminal = true
	p.run("plan", "owner/repo")

	creates := append(w.calls("sbx", "create"), p.calls("sbx", "create")...)
	if len(creates) < 7 {
		t.Fatalf("only %d sandboxes created", len(creates))
	}
	for _, c := range creates {
		if flagValue(c.Args, "--memory") == "" {
			t.Errorf("sandbox created without --memory: %v", c.Args)
		}
	}
}

func TestImplementerMemoryOverrideReachesItsSandboxOnly(t *testing.T) {
	w := newWorld(t)
	w.config += "implementer_memory = \"4g\"\n"
	w.run("owner/repo", "--ticket=42")

	got := map[string]string{}
	for _, c := range w.calls("sbx", "create") {
		role := "reviewer"
		if strings.Contains(flagValue(c.Args, "--name"), "-implementer-") {
			role = "implementer"
		}
		got[role] = flagValue(c.Args, "--memory")
	}
	if want := map[string]string{"implementer": "4g", "reviewer": "2g"}; !maps.Equal(got, want) {
		t.Errorf("memory limits = %v, want %v", got, want)
	}
}

func TestUnreadableImplementerMemoryIsRefusedNamingTheLine(t *testing.T) {
	for _, value := range []string{`"4"`, `"4gb"`, `"0g"`, `"4G"`, `4`, `"1.5g"`, `"4g" x`} {
		t.Run(value, func(t *testing.T) {
			w := newWorld(t)
			w.config += "implementer_memory = " + value + "\n"
			if code := w.run("owner/repo", "--n=1"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			w.assertStderrHas("config.toml:3:")
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestBlockedTicketStartsOnlyOnceItsBlockerClosesWhileOthersRun(t *testing.T) {
	w := newWorld(t)
	w.ticketOpened = day(0)
	w.addTicket(43, day(1)).blockedBy = []string{"owner/repo#42"}
	w.addTicket(44, day(2))
	w.closeDelay = 20 * time.Second
	w.hold = 2 // #42 and #44 run side by side
	w.holdFor = 200 * time.Millisecond

	if code := w.run("owner/repo", "--n=3"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if got, want := w.claimed(), []int{42, 44, 43}; !slices.Equal(got, want) {
		t.Fatalf("claimed %v, want %v\nstdout:\n%s", got, want, w.stdout.String())
	}
	if w.mostAtOnce < 2 {
		t.Errorf("%d agents ran at once, want #42 and #44 together", w.mostAtOnce)
	}
	// Closed means GitHub said so after the merge, not just that it merged.
	closedSeen := -1
	for i, c := range w.commands {
		if c.Name == "gh" && strings.Join(c.Args, " ") == "api repos/owner/repo/issues/42" && i > w.indexOf("gh", "pr", "merge", "7") {
			closedSeen = i
		}
	}
	claimed43 := slices.IndexFunc(w.commands, func(c lightsoff.Command) bool {
		return c.Name == "gh" && len(c.Args) > 3 && slices.Equal(c.Args[:3], []string{"issue", "edit", "43"}) && flagValue(c.Args, "--add-label") == "agent-in-progress"
	})
	if closedSeen < 0 || claimed43 < closedSeen {
		t.Errorf("#43 claimed at command %d, before #42 was seen closed at %d\nstdout:\n%s", claimed43, closedSeen, w.stdout.String())
	}
	w.assertEventOnce(`^#43 skipped: .*open blockers: #42`)
	w.assertEventOnce(`^#43 merged pull request #143`)
}

func TestAnotherTicketsMergeLeavingAConflictHandsBackThroughTheIteration(t *testing.T) {
	w := newWorld(t)
	w.addIndependentTickets(2)
	w.holdUntilMerged = 43 // #43 merges while #42's first pass runs
	w.conflictWhenMerged = 43

	if code := w.run("owner/repo", "--n=2"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	w.assertEventOnce(`^#43 merged pull request #143`)
	w.assertEventOnce(`^#42 handed back after iteration 1 of 3: pull request #7 has a merge conflict with main`)
	w.assertEventOnce(`^#42 merged pull request #7`)
	if w.implementerRuns != 2 {
		t.Errorf("#42's Implementer ran %d times, want 2", w.implementerRuns)
	}
}

var (
	statusLine   = regexp.MustCompile(`^#(\d+) `)
	sandboxNamed = regexp.MustCompile(`lo-repo-(\d+)-`)
	prNamed      = regexp.MustCompile(`pull request #(\d+)`)
)

func TestStatusLinesStayWithTheirTicketWhenTicketsRunAtOnce(t *testing.T) {
	w := newWorld(t)
	w.addIndependentTickets(5)
	w.others[44].verdict = oneWayVerdict
	w.hold = 5
	w.holdFor = 200 * time.Millisecond

	if code := w.run("owner/repo", "--n=5"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if w.mostAtOnce != 5 {
		t.Errorf("%d agents ran at once, want 5", w.mostAtOnce)
	}
	ends := map[int]string{}
	statusLines, _, _ := strings.Cut(w.stdout.String(), "lights-off: summary\n") // the summary is checked apart
	for line := range strings.SplitSeq(strings.TrimSpace(statusLines), "\n") {
		if strings.HasPrefix(line, "lights-off: ") {
			continue
		}
		m := statusLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("line belongs to no ticket: %q", line)
			continue
		}
		ticket := atoi(m[1])
		for _, s := range sandboxNamed.FindAllStringSubmatch(line, -1) {
			if atoi(s[1]) != ticket {
				t.Errorf("#%d names another ticket's sandbox: %q", ticket, line)
			}
		}
		pr := 100 + ticket
		if ticket == 42 {
			pr = 7
		}
		for _, p := range prNamed.FindAllStringSubmatch(line, -1) {
			if atoi(p[1]) != pr {
				t.Errorf("#%d names another ticket's pull request: %q", ticket, line)
			}
		}
		if strings.Contains(line, " merged ") || strings.Contains(line, " ready for human") {
			ends[ticket] = line
		}
	}
	for _, ticket := range []int{42, 43, 45, 46} {
		if !strings.Contains(ends[ticket], "merged") {
			t.Errorf("#%d ended %q, want merged", ticket, ends[ticket])
		}
	}
	if !strings.Contains(ends[44], "ready for human") {
		t.Errorf("#44 ended %q, want ready for human", ends[44])
	}
}

// A clone sandbox copies whatever the shared host checkout has at HEAD, so no
// ticket may move it between another's move and that one's sbx create.
func TestEachSandboxClonesItsOwnTicketsStartWhenTicketsRunAtOnce(t *testing.T) {
	w := newWorld(t)
	w.addIndependentTickets(5)
	w.hold = 5
	w.holdFor = 200 * time.Millisecond

	if code := w.run("owner/repo", "--n=5"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}
	if len(w.clonedFrom) != 10 {
		t.Fatalf("%d sandboxes created, want 10", len(w.clonedFrom))
	}
	for name, head := range w.clonedFrom {
		want := "refs/remotes/origin/main"
		if strings.Contains(name, "-reviewer-") {
			want = headSHA
			if ticket := sandboxTicket(name); ticket != 42 {
				want = otherHead(ticket)
			}
		}
		if head != want {
			t.Errorf("sandbox %s cloned the checkout at %q, want %q", name, head, want)
		}
	}
}

func TestStartLineNamesEachTicketsPeakMemory(t *testing.T) {
	w := newWorld(t)
	w.config += "implementer_memory = \"512m\"\n"
	w.hostMemory = 8 << 30
	w.run("owner/repo", "--n=5")
	// The Reviewer's 2g is the larger, because the Implementer's is stopped while it runs.
	w.assertEventOnce(`^lights-off: up to 3 tickets at once: 75% of 8.0 GB host memory at 2g each`)
}
