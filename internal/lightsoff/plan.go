package lightsoff

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// sessionSandboxName is fixed per Target Repository, so that running plan again
// finds the same sandbox. Two repositories can share a slug, through punctuation
// or a cut-off name, so a hash of the whole name keeps them apart. GitHub names
// ignore case, and so does the hash.
func sessionSandboxName(repo string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(repo)))
	return fmt.Sprintf("lo-plan-%.40s-%x", slug(repo), sum[:4])
}

// githubDomains are what the dsh kit's network policy allows for GitHub. The
// built-in claude agent's policy doesn't cover them, and under a deny-by-default
// policy the proxy would answer 403 however good the token.
var githubDomains = []string{"api.github.com", "github.com", "*.github.com", "raw.githubusercontent.com", "*.githubusercontent.com"}

// planningPlugin carries grill-me, to-spec, to-tickets and
// setup-matt-pocock-skills, which a Planning Session is driven with. The built-in
// claude agent ships without them, and without the marketplace that lists them:
// it knows only its built-in directory. Anthropic's official marketplace pins
// the plugin to a commit of mattpocock/skills.
const (
	planningMarketplace = "anthropics/claude-plugins-official"
	planningPlugin      = "mattpocock-skills@claude-plugins-official"
)

// planningGrants lets to-tickets publish. Claude Code signs in through sbx's own
// credential flow, so lights-off delivers nothing for it.
var planningGrants = []grant{{"github", "GITHUB_TOKEN"}}

// openPlanningSession opens Claude Code in a sandbox on the Target Repository and
// steps aside: the person drives the session themselves. The Planning Branch is
// theirs to push and merge, so lights-off does neither.
//
// Unlike an Autonomous Run it merges no credential bindings and checks for no
// set-custom support: the claude agent is built into sbx rather than a local
// kit, and the session gets no repository secrets.
func openPlanningSession(repo string, cfg config, env Env, enableDashboard bool) error {
	if !env.IsTerminal() {
		return errors.New("plan opens Claude Code for a person to drive, so it needs a terminal")
	}
	sh := shell{exec: env.Exec}
	values, err := loadSecrets(env.ConfigDir, cfg, repo, planningGrants)
	if err != nil {
		return err
	}
	mounts, err := sh.sandboxMounts()
	if err != nil {
		return err
	}
	mounts = append(mounts, checkoutDir(env.CacheDir, repo))
	for _, path := range []string{secretsPath(env.ConfigDir), configPath(env.ConfigDir)} {
		if err := checkNotMounted(path, mounts); err != nil {
			return err
		}
	}

	name := sessionSandboxName(repo)
	names, err := sh.run("sbx", "ls", "-q")
	if err != nil {
		return err
	}
	if slices.Contains(strings.Fields(names), name) {
		fmt.Fprintf(env.Stdout, "resuming the Planning Session in sandbox %s\n", name)
	} else if err := createSessionSandbox(sh, repo, name, env); err != nil {
		return err
	}
	if err := installPlanningSkills(sh, name, checkoutDir(env.CacheDir, repo)); err != nil {
		return err
	}
	// Stored on every open, so that a rotated token or one an earlier open
	// failed to store reaches the session too. sbx secret set won't overwrite,
	// and a removed sandbox of the same name can have left its secret behind,
	// so whatever is there goes first. Nothing being there fails the removal,
	// and any other failure shows when the set fails too.
	for _, g := range planningGrants {
		_, _ = sh.run("sbx", "secret", "rm", g.service, "--sandbox", name, "--force")
	}
	if err := (sandboxes{shell: sh}).deliver(&sandbox{name: name}, planningGrants, nil, values); err != nil {
		return err
	}

	// The Dashboard lives only while Claude Code is open: sbx run returns when
	// the person's session ends.
	if d := startDashboard(env, repo, "lights-off plan "+repo, cfg.repos[repo].allowlist, enableDashboard); d != nil {
		defer d.Close()
		// The page learns the session is over from the stream itself, before the
		// listener stops. Defers run in reverse, so this is published first.
		defer d.publish(Event{Kind: EventSessionEnded})
	}
	fmt.Fprintln(env.Stdout, "opening Claude Code; lights-off won't push or merge the Planning Branch, so push it from the session when it's ready")
	res, err := env.Exec(Command{Name: "sbx", Args: []string{"run", "--name", name}, Attach: true})
	if err != nil {
		return fmt.Errorf("sbx run: %w", err)
	}
	// Any other status is how the person's session ended, which is theirs.
	if res.ExitCode == sbxExecFailed {
		return fmt.Errorf("sbx run exited %d, so the session couldn't open; sandbox %s is kept", res.ExitCode, name)
	}
	fmt.Fprintf(env.Stdout, "sandbox kept: %s. Run lights-off plan %s to resume\n", name, repo)
	return nil
}

// installPlanningSkills adds planningMarketplace and installs planningPlugin for
// the sandbox's user, so the skills stay out of the Planning Branch. Both steps
// are safe to repeat, and run on every open so that a sandbox whose install
// failed, or one made before lights-off installed anything, gets the skills when
// resumed. Both fetch from GitHub, which the sandbox's network rule allows.
func installPlanningSkills(sh shell, name, workspace string) error {
	for _, args := range [][]string{
		{"marketplace", "add", planningMarketplace},
		{"install", planningPlugin},
	} {
		cmd := append([]string{"exec", "-w", workspace, name, "claude", "plugin"}, args...)
		if _, err := sh.run("sbx", cmd...); err != nil {
			return fmt.Errorf("installing Matt Pocock's skills in sandbox %s: %w", name, err)
		}
	}
	return nil
}

// createSessionSandbox starts a new Planning Branch for the sandbox to clone. A
// clone sandbox starts at whatever the checkout has at HEAD.
func createSessionSandbox(sh shell, repo, name string, env Env) error {
	co, err := openCheckout(sh, env.CacheDir, repo)
	if err != nil {
		return err
	}
	info, err := sh.readRepo(repo)
	if err != nil {
		return err
	}
	base := info.DefaultBranchRef.Name
	branch := "planning/" + env.Clock.Now().Format(time.DateOnly)
	if err := co.startBranch(branch, base); err != nil {
		return err
	}
	if _, err := sh.run("sbx", "create", "--clone", "--name", name, "--memory", planningMemory, "claude", co.dir); err != nil {
		return err
	}
	// The rule goes with the sandbox, so it's added once, as the sandbox is made.
	if _, err := sh.run("sbx", "policy", "allow", "network", "--sandbox", name, strings.Join(githubDomains, ",")); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "Planning Branch %s started from %s in sandbox %s\n", branch, base, name)
	return nil
}
