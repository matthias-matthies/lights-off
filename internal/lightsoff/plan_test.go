package lightsoff_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPlanOpensClaudeCodeInASandboxOnTheCheckout(t *testing.T) {
	w := newWorld(t)
	w.terminal = true

	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, w.stdout.String(), w.stderr.String())
	}

	creates := w.calls("sbx", "create")
	if len(creates) != 1 {
		t.Fatalf("sbx create = %v, want one", creates)
	}
	args := creates[0].Args
	if !slices.Contains(args, "--clone") || args[len(args)-2] != "claude" {
		t.Errorf("want a clone sandbox for the claude agent, got %v", args)
	}
	name := flagValue(args, "--name")
	runs := w.calls("sbx", "run")
	if len(runs) != 1 || flagValue(runs[0].Args, "--name") != name || !runs[0].Attach {
		t.Errorf("want one attached sbx run --name %s, got %v", name, runs)
	}
	// Claude Code opens as the person's own session: lights-off passes it nothing to do.
	if slices.Contains(runs[0].Args, "--") {
		t.Errorf("lights-off scripted the session: %v", runs[0].Args)
	}
}

func TestPlanStartsAPlanningBranchFromTheDefaultBranch(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.defaultBranch = "trunk"

	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	fetch := w.indexOf("git", "fetch", "+refs/heads/trunk:refs/remotes/origin/trunk")
	branch := w.indexOf("git", "checkout", "planning/2026-11-06", "refs/remotes/origin/trunk")
	create := w.indexOf("sbx", "create")
	if fetch < 0 || branch < fetch || create < branch {
		t.Errorf("want trunk fetched, then planning/2026-11-06 made from it, then the sandbox created; got %v", w.commands)
	}
}

func TestPlanAgainReattachesToTheSameSandbox(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("first exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	first := flagValue(w.calls("sbx", "run")[0].Args, "--name")

	w.now = w.now.Add(24 * time.Hour) // resumed the next day
	w.commands = nil
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("second exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("a second sandbox was created: %v", got)
	}
	if got := w.calls("git"); len(got) != 0 {
		t.Errorf("the checkout was touched on reattach: %v", got)
	}
	runs := w.calls("sbx", "run")
	if len(runs) != 1 || flagValue(runs[0].Args, "--name") != first || !runs[0].Attach {
		t.Errorf("want an attached sbx run --name %s, got %v", first, runs)
	}
}

func TestPlanForAnotherOwnersRepositoryOfTheSameNameGetsItsOwnSandbox(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.config += "[repos.\"other/repo\"]\nallowlist = [\"other\"]\n"
	w.run("plan", "owner/repo")
	w.run("plan", "other/repo")

	creates := w.calls("sbx", "create")
	if len(creates) != 2 || flagValue(creates[0].Args, "--name") == flagValue(creates[1].Args, "--name") {
		t.Errorf("want two sandboxes with different names, got %v\nstderr:\n%s", creates, w.stderr.String())
	}
}

func TestPlanGivesTheSandboxOnlyTheGitHubTokenThroughTheProxy(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	name := flagValue(w.calls("sbx", "create")[0].Args, "--name")
	// Claude Code signs in through sbx's own flow, so lights-off stores nothing else.
	want := map[string]string{"github": implementerGH}
	if got := w.scoped[name]; !maps.Equal(got, want) {
		t.Errorf("scoped secrets = %v, want %v", got, want)
	}
	if set, run := w.indexOf("sbx", "secret", "set"), w.indexOf("sbx", "run"); set > run {
		t.Errorf("token stored after the session opened")
	}
	for _, c := range w.commands {
		for _, arg := range c.Args {
			if arg == "-e" || strings.HasPrefix(arg, "--env") ||
				strings.Contains(arg, implementerGH) || strings.Contains(arg, deepseekKey) || strings.Contains(arg, reviewerGH) {
				t.Errorf("secret or env flag in arguments: %v", c)
			}
		}
	}
}

func TestPlanRefusesWhenSecretsAreUnsafe(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.secretsPerm = 0o644

	if code := w.run("plan", "owner/repo"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created: %v", got)
	}
	w.assertStderrHas("0600")
}

func TestPlanForARepositoryNotOptedInIsRefusedWithTheLinesToAdd(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

	if code := w.run("plan", "owner/repo"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	w.assertStderrHas(`[repos."owner/repo"]`)
	if len(w.commands) != 0 {
		t.Errorf("commands sent for a repository not opted in: %v", w.commands)
	}
}

func TestPlanLeavesPushingAndMergingThePlanningBranchToThePerson(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	for _, c := range w.commands {
		line := strings.Join(append([]string{c.Name}, c.Args...), " ")
		if strings.Contains(line, "push") || strings.HasPrefix(line, "gh pr") {
			t.Errorf("lights-off pushed or touched a pull request: %s", line)
		}
	}
	out := w.stdout.String()
	name := flagValue(w.calls("sbx", "create")[0].Args, "--name")
	for _, want := range []string{"planning/2026-11-06", name, "lights-off plan owner/repo"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if got := w.calls("sbx", "rm"); len(got) != 0 {
		t.Errorf("the Planning Session's sandbox was removed: %v", got)
	}
}

func TestPlanWithoutATerminalIsRefused(t *testing.T) {
	w := newWorld(t)

	if code := w.run("plan", "owner/repo"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent with nobody to drive the session: %v", w.commands)
	}
	w.assertStderrHas("terminal")
}

func TestPlanFailsWhenSbxCannotOpenTheSession(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.sessionExit = 125

	if code := w.run("plan", "owner/repo"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	w.assertStderrHas("sbx run")
}

func TestPlanTakesNeitherTicketNorN(t *testing.T) {
	for _, flag := range []string{"--ticket=42", "--n=5"} {
		t.Run(flag, func(t *testing.T) {
			w := newWorld(t)
			w.terminal = true
			if code := w.run("plan", "owner/repo", flag); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestPlanNeedsOnlyTheGitHubToken(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.secrets = "GITHUB_TOKEN=" + implementerGH + "\n"

	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
}

func TestPlanForRepositoriesWhoseNamesLookAlikeGetsSeparateSandboxes(t *testing.T) {
	long := strings.Repeat("x", 60)
	cases := map[string][2]string{
		"punctuation": {"owner/a.b", "owner/a-b"},
		"long names":  {"owner/" + long + "1", "owner/" + long + "2"},
	}
	for name, repos := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.terminal = true
			w.config = ""
			for _, repo := range repos {
				w.config += "[repos.\"" + repo + "\"]\nallowlist = [\"owner\"]\n"
			}
			w.run("plan", repos[0])
			w.run("plan", repos[1])

			creates := w.calls("sbx", "create")
			if len(creates) != 2 || flagValue(creates[0].Args, "--name") == flagValue(creates[1].Args, "--name") {
				t.Errorf("want two sandboxes with different names, got %v\nstderr:\n%s", creates, w.stderr.String())
			}
		})
	}
}

func TestPlanRefusesWhenItsConfigIsMountedIntoASandbox(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	w.listing = `[{"name":"other","mounts":["` + w.configDir + `/config.toml:/config.toml:ro"]}]`

	if code := w.run("plan", "owner/repo"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created: %v", got)
	}
	w.assertStderrHas("config.toml")
}

// The built-in claude agent's network policy doesn't cover GitHub, unlike the
// dsh kit's, so a stored token alone gets the proxy's 403.
func TestPlanLetsTheSandboxReachGitHub(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	name := flagValue(w.calls("sbx", "create")[0].Args, "--name")
	allows := w.calls("sbx", "policy", "allow", "network")
	if len(allows) != 1 || flagValue(allows[0].Args, "--sandbox") != name {
		t.Fatalf("want one network allow scoped to %s, got %v", name, allows)
	}
	domains := strings.Split(allows[0].Args[len(allows[0].Args)-1], ",")
	for _, want := range []string{"api.github.com", "github.com", "*.github.com", "raw.githubusercontent.com", "*.githubusercontent.com"} {
		if !slices.Contains(domains, want) {
			t.Errorf("allowed domains %v lack %s", domains, want)
		}
	}
	if allow, run := w.indexOf("sbx", "policy", "allow"), w.indexOf("sbx", "run"); allow > run {
		t.Errorf("GitHub allowed only after the session opened")
	}
}

// sbx rm can leave a sandbox's scoped secret behind, and a reattach finds the
// one it stored itself; sbx secret set won't overwrite either.
func TestPlanReplacesATokenAlreadyStoredForTheSandbox(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	name := "lo-plan-owner-repo-65e817ee"
	w.scoped[name] = map[string]string{"github": "ghp-stale-value"}

	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got := w.scoped[name]["github"]; got != implementerGH {
		t.Errorf("stored token = %q, want the one in secrets.env", got)
	}
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("reattach exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
}

// grill-me, to-spec and to-tickets are what the person drives the session
// with, and the built-in claude agent comes without them or the marketplace
// that lists them.
func TestPlanInstallsMattPococksSkillsInTheSandbox(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	name := flagValue(w.calls("sbx", "create")[0].Args, "--name")
	assertPluginSteps(t, w, name)
	allow, update, install, run := w.indexOf("sbx", "policy", "allow"), w.indexOf("sbx", "exec", "marketplace"), w.indexOf("sbx", "exec", "install"), w.indexOf("sbx", "run")
	if allow > update || update > install || install > run {
		t.Errorf("want GitHub allowed, the marketplace added, the plugin installed, then the session opened; got %v", w.commands)
	}
}

// A sandbox whose install failed, or one made before lights-off installed the
// skills, gets them when the session is resumed.
func TestPlanAgainInstallsMattPococksSkillsToo(t *testing.T) {
	w := newWorld(t)
	w.terminal = true
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	name := flagValue(w.calls("sbx", "create")[0].Args, "--name")

	w.commands = nil
	if code := w.run("plan", "owner/repo"); code != 0 {
		t.Fatalf("reattach exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	assertPluginSteps(t, w, name)
}

func assertPluginSteps(t *testing.T, w *world, name string) {
	t.Helper()
	var steps []string
	for _, c := range w.calls("sbx", "exec") {
		if c.Args[3] == name && slices.Contains(c.Args, "plugin") {
			steps = append(steps, strings.Join(c.Args[4:], " "))
		}
	}
	want := []string{
		"claude plugin marketplace add anthropics/claude-plugins-official",
		"claude plugin install mattpocock-skills@claude-plugins-official",
	}
	if !slices.Equal(steps, want) {
		t.Errorf("plugin steps in %s = %q, want %q", name, steps, want)
	}
}
