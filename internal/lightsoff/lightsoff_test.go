package lightsoff_test

import (
	"slices"
	"strings"
	"testing"
)

func TestTicketAndNTogetherOrNeitherIsAnError(t *testing.T) {
	cases := map[string][]string{
		"both":    {"owner/repo", "--ticket=42", "--n=5"},
		"neither": {"owner/repo"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			if code := w.run(args...); code == 0 {
				t.Fatalf("exit code = 0, want non-zero")
			}
			if !strings.Contains(w.stderr.String(), "--ticket") || !strings.Contains(w.stderr.String(), "--n") {
				t.Errorf("stderr should name both flags, got %q", w.stderr.String())
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestVersionPrintsTheReleaseAndTouchesNothing(t *testing.T) {
	cases := map[string]struct{ version, want string }{
		"release": {"v0.1.0", "lights-off v0.1.0\n"},
		"unset":   {"", "lights-off dev\n"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.version = c.version
			if code := w.run("--version"); code != 0 {
				t.Fatalf("exit code = %d, want 0; stderr %q", code, w.stderr.String())
			}
			if got := w.stdout.String(); got != c.want {
				t.Errorf("stdout = %q, want %q", got, c.want)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestVersionWithOtherArgumentsIsAnError(t *testing.T) {
	w := newWorld(t)
	if code := w.run("owner/repo", "--version"); code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepositoryNotInConfigIsRefusedWithTheCommandToOptItIn(t *testing.T) {
	w := newWorld(t)
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo add owner/repo") {
		t.Errorf("stderr should print the command to opt it in, got %q", w.stderr.String())
	}
	if strings.Contains(w.stderr.String(), `[repos."owner/repo"]`) {
		t.Errorf("stderr should not print config lines, got %q", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent to a repository not opted in: %v", w.commands)
	}
}

func TestOneTicketGoesFromStartToMerge(t *testing.T) {
	w := newWorld(t)

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	merges := w.calls("gh", "pr", "merge")
	if len(merges) != 1 {
		t.Fatalf("merge commands = %v, want one", merges)
	}
	args := merges[0].Args
	if flagValue(args, "--match-head-commit") != headSHA {
		t.Errorf("merge not pinned to the reviewed commit: %v", args)
	}
	if !slices.Contains(args, "--squash") {
		t.Errorf("merge should follow the repository default (squash): %v", args)
	}

	if len(w.sandboxes) != 2 || w.sandboxes[0] == w.sandboxes[1] {
		t.Fatalf("sandboxes = %v, want two distinct", w.sandboxes)
	}
	removed := w.calls("sbx", "rm")
	if len(removed) != 2 {
		t.Fatalf("sbx rm calls = %v, want two", removed)
	}
	for _, name := range w.sandboxes {
		i := w.indexOf("sbx", "rm", name)
		if i < 0 {
			t.Errorf("sandbox %q not removed", name)
		}
		if i < w.indexOf("gh", "pr", "merge") {
			t.Errorf("sandbox %q removed before the merge", name)
		}
	}

	for _, event := range []string{
		`^#42 implementer started`,
		`^#42 pull request #7 opened`,
		`^#42 CI green`,
		`^#42 reviewer started`,
		`^#42 verdict: mergeable, low risk, two-way`,
		`^#42 merged pull request #7`,
		`^#42 sandboxes removed`,
	} {
		w.assertEventOnce(event)
	}
}

func TestImplementerRunsHeadlessInAFreshSandboxWithTheTicketText(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	create := w.calls("sbx", "create")[0].Args
	if !slices.Contains(create, "--clone") || flagValue(create, "--memory") != "2g" {
		t.Errorf("implementer sandbox create = %v", create)
	}

	exec := w.calls("sbx", "exec")[0].Args
	for _, flag := range [][]string{
		{"--profile", "headless"},
		{"--permission-mode", "danger-full-access"},
		{"--approval-policy", "never"},
	} {
		if flagValue(exec, flag[0]) != flag[1] {
			t.Errorf("dsh missing %v: %v", flag, exec)
		}
	}

	prompt := w.prompt(w.sandboxes[0])
	for _, want := range []string{
		"Make sum add numbers.",
		"implement skill",
		"pr skill",
		"Closes #42",
		"Do not run the code-review skill",
		"Do not merge",
		`"status"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("implementer prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestReviewerRunsInANewSandboxOnThePullRequestWithItsFixedPointAndTicket(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	create := w.calls("sbx", "create")[1].Args
	if !slices.Contains(create, "--clone") || flagValue(create, "--memory") != "2g" {
		t.Errorf("reviewer sandbox create = %v", create)
	}
	// A clone sandbox starts at the host checkout's HEAD, so the checkout must
	// be on the commit under review before the sandbox is created.
	checkout := w.indexOf("git", "checkout", headSHA)
	if checkout < 0 || checkout > w.indexOf("sbx", "create", flagValue(create, "--name")) {
		t.Errorf("checkout of %s must precede the reviewer sandbox: %v", headSHA, w.commands)
	}

	prompt := w.prompt(w.sandboxes[1])
	for _, want := range []string{
		"code-review skill",
		mergeBase,
		"Make sum add numbers.",
		"Do not merge",
		"Do not ask",
		`"mergeable"`,
	} {
		if !strings.Contains(prompt, want) {
			t.Errorf("reviewer prompt lacks %q:\n%s", want, prompt)
		}
	}
}

func TestExistingCheckoutIsReused(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")
	w.run("owner/repo", "--ticket=42")

	if got := w.calls("gh", "repo", "clone"); len(got) != 1 {
		t.Errorf("clones = %v, want one across two runs", got)
	}
}
