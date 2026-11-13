package lightsoff_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

func TestSecretsFileThatIsNot0600IsRefusedBeforeAnythingRuns(t *testing.T) {
	for name, perm := range map[string]os.FileMode{
		"group readable":  0o640,
		"world readable":  0o644,
		"group writable":  0o620,
		"read-only owner": 0o400,
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.secretsPerm = perm

			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(w.stderr.String(), "secrets.env") || !strings.Contains(w.stderr.String(), "0600") {
				t.Errorf("stderr should name the file and the mode, got %q", w.stderr.String())
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestMissingSecretsFileIsRefusedNamingIt(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(w.stderr.String(), "secrets.env") {
		t.Errorf("stderr should name the file, got %q", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestSecretsDirectoryInsideASandboxMountIsRefused(t *testing.T) {
	cases := map[string]func(w *world){
		"inside the Target Repository's checkout": func(w *world) {
			w.configDir = mkdirs(t, w.cacheDir, "repos", "owner", "repo", ".config")
		},
		"is the Target Repository's checkout": func(w *world) {
			w.configDir = mkdirs(t, w.cacheDir, "repos", "owner", "repo")
		},
		"inside the kit": func(w *world) {
			w.configDir = mkdirs(t, w.cacheDir, "kits", "dsh", "conf")
		},
		"inside another sandbox's workspace": func(w *world) {
			w.listing = sandboxListing(w.configDir)
		},
		"under another sandbox's workspace": func(w *world) {
			w.listing = sandboxListing(filepath.Dir(w.configDir))
		},
		"under a read-only mount": func(w *world) {
			dir := filepath.Dir(w.configDir)
			w.listing = sandboxListingOf([]string{dir + ":" + dir + ":ro"}, []string{dir + ":ro"})
		},
		"under a mount at another path in the sandbox": func(w *world) {
			dir := filepath.Dir(w.configDir)
			mount := dir + ":/workspace/data:ro"
			w.listing = sandboxListingOf([]string{mount}, []string{mount})
		},
		"listed only under mounts": func(w *world) {
			w.listing = sandboxListingOf([]string{w.configDir + ":/run/sbx/skills-shared:ro"}, nil)
		},
		"listed only under workspaces": func(w *world) {
			w.listing = sandboxListingOf(nil, []string{w.configDir})
		},
		"under a mount with a space in its path": func(w *world) {
			w.configDir = mkdirs(t, t.TempDir(), "Application Support", "conf")
			w.listing = sandboxListing(filepath.Dir(w.configDir))
		},
		"under a mount with a colon in its name": func(w *world) {
			w.configDir = mkdirs(t, t.TempDir(), "work:old")
			w.listing = sandboxListing(w.configDir)
		},
		"under a mount with a colon higher in its path": func(w *world) {
			w.configDir = mkdirs(t, t.TempDir(), "work:old", "conf")
			w.listing = sandboxListing(filepath.Dir(w.configDir))
		},
		"under a read-only mount with a colon in its name": func(w *world) {
			w.configDir = mkdirs(t, t.TempDir(), "work:old")
			mount := w.configDir + ":/workspace/data:ro"
			w.listing = sandboxListingOf([]string{mount}, []string{mount})
		},
		"the file itself is mounted": func(w *world) {
			w.listing = sandboxListing(filepath.Join(w.configDir, "secrets.env"))
		},
		"reached through a symlinked mount": func(w *world) {
			link := filepath.Join(t.TempDir(), "link")
			if err := os.Symlink(w.configDir, link); err != nil {
				t.Fatal(err)
			}
			w.listing = sandboxListing(link)
		},
		"file is a symlink into a mount": func(w *world) {
			mounted := t.TempDir()
			real := filepath.Join(mounted, "real.env")
			if err := os.WriteFile(real, []byte(coreSecrets), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(w.configDir, "secrets.env")); err != nil {
				t.Fatal(err)
			}
			w.skipSecrets = true
			w.listing = sandboxListing(mounted)
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			arrange(w)

			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(w.stderr.String(), "mounted") {
				t.Errorf("stderr should explain the mount, got %q", w.stderr.String())
			}
			if got := w.calls("sbx", "create"); len(got) != 0 {
				t.Errorf("sandbox created: %v", got)
			}
			if got := w.calls("gh"); len(got) != 0 {
				t.Errorf("gh commands sent: %v", got)
			}
		})
	}
}

func TestUnrelatedSandboxMountsAreFine(t *testing.T) {
	cases := map[string]func(w *world){
		"another folder": func(w *world) {
			w.listing = sandboxListing(t.TempDir())
		},
		"another folder with a colon in its name": func(w *world) {
			w.listing = sandboxListing(mkdirs(t, t.TempDir(), "work:old"))
		},
		// Only the host side of a mount exposes host files.
		"the secrets path used as a target in the sandbox": func(w *world) {
			w.listing = sandboxListingOf([]string{t.TempDir() + ":" + w.configDir}, nil)
		},
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			arrange(w)

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
		})
	}
}

func TestUnreadableSandboxListingIsRefused(t *testing.T) {
	w := newWorld(t)
	w.listing = "NAME  WORKSPACE\n"

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created: %v", got)
	}
}

func mkdirs(t *testing.T, parts ...string) string {
	t.Helper()
	dir := filepath.Join(parts...)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sandboxListing is sbx ls --json output for one sandbox mounting path at the
// same path inside it.
func sandboxListing(path string) string {
	return sandboxListingOf([]string{path + ":" + path}, []string{path})
}

// sandboxListingOf is sbx ls --json output for one sandbox, in the shape a real
// sbx prints. A nil list leaves that field out.
func sandboxListingOf(mounts, workspaces []string) string {
	sandbox := map[string]any{
		"name":         "other",
		"id":           "25776bdc-7615-4066-9b9d-e98be70e87d2",
		"agent":        "claude",
		"status":       "running",
		"last_used_at": "2026-11-07T11:29:44.54846Z",
		"created_at":   "2026-11-06T15:04:38Z",
	}
	if mounts != nil {
		sandbox["mounts"] = mounts
	}
	if workspaces != nil {
		sandbox["workspaces"] = workspaces
	}
	listing, err := json.Marshal([]any{sandbox})
	if err != nil {
		panic(err)
	}
	return string(listing)
}

func TestSecretsReachOnlyTheSandboxesWhoseRoleNeedsThem(t *testing.T) {
	w := newWorld(t)
	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}

	implementer, reviewer := w.sandboxes[0], w.sandboxes[1]
	want := map[string]map[string]string{
		implementer: {"deepseek": deepseekKey, "github": implementerGH},
		reviewer:    {"deepseek": deepseekKey, "github": reviewerGH},
	}
	for _, c := range w.calls("sbx", "secret", "set") {
		sandbox, service := flagValue(c.Args, "--sandbox"), c.Args[2]
		if c.Stdin != want[sandbox][service] {
			t.Errorf("%s in %q got value %q", service, sandbox, c.Stdin)
		}
		delete(want[sandbox], service)
	}
	for sandbox, missing := range want {
		if len(missing) != 0 {
			t.Errorf("%q never received %v", sandbox, missing)
		}
	}
}

func TestSecretsArriveBeforeTheAgentRuns(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	for _, name := range w.sandboxes {
		agent := w.indexOf("sbx", "exec", name, "dsh")
		create := w.indexOf("sbx", "create", name)
		for _, service := range []string{"deepseek", "github"} {
			set := w.indexOf("sbx", "secret", "set", service, name)
			if set < create || set > agent {
				t.Errorf("%s for %q at %d, want between create %d and agent %d", service, name, set, create, agent)
			}
		}
	}
}

func TestSecretValuesNeverAppearInArgumentsOrOutput(t *testing.T) {
	cases := map[string]func(w *world){
		"a run that merges":                  func(*world) {},
		"sbx rejects a secret and quotes it": func(w *world) { w.secretSetExit = 1 },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			arrange(w)
			w.run("owner/repo", "--ticket=42")

			for _, value := range []string{deepseekKey, implementerGH, reviewerGH} {
				for _, c := range w.commands {
					if strings.Contains(strings.Join(c.Args, " "), value) {
						t.Errorf("value in arguments: %v", c)
					}
				}
				if strings.Contains(w.stdout.String()+w.stderr.String(), value) {
					t.Errorf("value in output:\n%s\n%s", w.stdout.String(), w.stderr.String())
				}
			}
		})
	}
}

func TestSecretThatCannotBeDeliveredStopsTheAttempt(t *testing.T) {
	w := newWorld(t)
	w.secretSetExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.indexOf("sbx", "exec", "dsh"); got >= 0 {
		t.Errorf("agent ran without its credentials: %v", w.commands[got])
	}
	w.assertNoMerge()
	w.assertSandboxesKept()
}

func TestEnvFlagsAndOAuthPassthroughAreNeverUsed(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "STRIPE_KEY=" + stripeKey + "\n"
	w.config = stripeConfig
	w.run("owner/repo", "--ticket=42")

	for _, c := range w.commands {
		for _, arg := range c.Args {
			if arg == "-e" || strings.HasPrefix(arg, "--env-file") {
				t.Errorf("%q used in %v", arg, c)
			}
		}
	}
	spec, err := os.ReadFile(filepath.Join(w.cacheDir, "kits", "dsh", "dsh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(spec), "passthrough") {
		t.Errorf("kit uses OAuth passthrough")
	}
}

// sbx 0.51.0 made shell a v3 kit. A v2 kit can't extend it, and on sbx 0.53.0
// one naming shell's image fails to create, so the kit is a v3 mixin added to
// shell. shell declares no credentials, so the kit brings its own github service.
func TestKitIsAV3MixinForShell(t *testing.T) {
	w := newWorld(t)
	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	dir := filepath.Join(w.cacheDir, "kits", "dsh")
	if _, err := os.Stat(filepath.Join(dir, "spec.yaml")); err == nil {
		t.Errorf("kit still has a v2 spec.yaml")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "dsh.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	spec := string(raw)
	for _, want := range []string{
		"# syntax=docker/sandbox-kit:3\n", "schemaVersion: \"3\"", "kind: mixin",
		"service: deepseek", "service: github", "name: GH_TOKEN",
	} {
		if !strings.Contains(spec, want) {
			t.Errorf("kit should contain %q", want)
		}
	}
	for _, unwanted := range []string{"\nextends:", "\nsandbox:", "\n  image:", "scheme:"} {
		if strings.Contains(spec, unwanted) {
			t.Errorf("kit should not contain %q", unwanted)
		}
	}
}

func TestMergeUsesGitHubTokenOnTheHost(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	merges := w.calls("gh", "pr", "merge")
	if len(merges) != 1 {
		t.Fatalf("merges = %v", merges)
	}
	if !slices.Contains(merges[0].Env, "GH_TOKEN="+implementerGH) {
		t.Errorf("merge environment = %q", merges[0].Env)
	}
	for _, c := range w.commands {
		if len(c.Env) != 0 && !slices.Equal(c.Args[:2], []string{"pr", "merge"}) {
			t.Errorf("token in the environment of %v", c)
		}
	}
}

func TestSecretsFileThatCannotBeReadIsRefusedWithoutEchoingIt(t *testing.T) {
	cases := map[string]struct{ secrets, want string }{
		"missing key":    {"GITHUB_TOKEN=a\nGITHUB_REVIEWER_TOKEN=b\n", "DEEPSEEK_API_KEY"},
		"empty value":    {"DEEPSEEK_API_KEY=\nGITHUB_TOKEN=a\nGITHUB_REVIEWER_TOKEN=b\n", "DEEPSEEK_API_KEY"},
		"not an entry":   {"# keys\n\nsk-hunter2\n" + coreSecrets, "secrets.env:3"},
		"repeated key":   {coreSecrets + "GITHUB_TOKEN=sk-hunter2\n", "secrets.env:4"},
		"inline comment": {"DEEPSEEK_API_KEY=sk-hunter2 # work\nGITHUB_TOKEN=a\nGITHUB_REVIEWER_TOKEN=b\n", "secrets.env:1"},
		"no domains":     {coreSecrets + "DB_PASSWORD=sk-hunter2\n", "DB_PASSWORD"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.secrets = tc.secrets

			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if !strings.Contains(w.stderr.String(), tc.want) {
				t.Errorf("stderr should contain %q, got %q", tc.want, w.stderr.String())
			}
			if strings.Contains(w.stderr.String(), "hunter2") {
				t.Errorf("stderr echoes the file: %q", w.stderr.String())
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestSecretsFileAcceptsCommentsQuotesAndWindowsEndings(t *testing.T) {
	w := newWorld(t)
	w.secrets = "# keys\r\n\r\nDEEPSEEK_API_KEY=\"" + deepseekKey + "\"\r\nGITHUB_TOKEN='" + implementerGH + "'\r\n" +
		"GITHUB_REVIEWER_TOKEN=" + reviewerGH + "\r\n"

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s\nstdout:\n%s", code, w.stderr.String(), w.stdout.String())
	}
	got := map[string]string{}
	for _, c := range w.calls("sbx", "secret", "set") {
		got[c.Args[2]+"@"+flagValue(c.Args, "--sandbox")] = c.Stdin
	}
	if got["deepseek@"+w.sandboxes[0]] != deepseekKey || got["github@"+w.sandboxes[0]] != implementerGH ||
		got["github@"+w.sandboxes[1]] != reviewerGH {
		t.Errorf("values = %v", got)
	}
}

func TestRemovalThatFailsItsSecretCleanupIsRetriedAndThenSucceeds(t *testing.T) {
	w := newWorld(t)
	w.rmExit = 1 // the sandbox goes, but the keychain refuses to delete its secrets

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
	}
	for _, name := range w.sandboxes {
		for _, service := range []string{"deepseek", "github"} {
			if w.indexOf("sbx", "secret", "rm", service, "--sandbox", name) < 0 {
				t.Errorf("no scoped sbx secret rm %s for %q: %v", service, name, w.commands)
			}
		}
		if len(w.scoped[name]) != 0 {
			t.Errorf("secrets left behind for %q: %v", name, w.scoped[name])
		}
	}
	w.assertEventOnce(`^#42 sandboxes removed`)
}

func TestRetriedSecretCleanupThatStillFailsIsReported(t *testing.T) {
	w := newWorld(t)
	w.rmExit = 1
	w.secretRmExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	out := w.stdout.String()
	if strings.Contains(out, "sandboxes removed") {
		t.Errorf("reported as clean:\n%s", out)
	}
	for _, name := range w.sandboxes {
		if !strings.Contains(out, "sbx secret rm deepseek --sandbox "+name) {
			t.Errorf("should say how to remove what's left of %q:\n%s", name, out)
		}
	}
	for _, value := range []string{deepseekKey, implementerGH, reviewerGH} {
		if strings.Contains(out, value) {
			t.Errorf("value in output:\n%s", out)
		}
	}
}

func TestSecretsAreLeftAloneWhenTheSandboxSurvivesRemoval(t *testing.T) {
	w := newWorld(t)
	w.rmExit = 1
	w.rmLeavesSandbox = true

	w.run("owner/repo", "--ticket=42")

	if got := w.calls("sbx", "secret", "rm"); len(got) != 0 {
		t.Errorf("removed secrets of a sandbox still there: %v", got)
	}
}

func TestSuccessfulRemovalNeedsNoSecretCleanup(t *testing.T) {
	w := newWorld(t)
	w.run("owner/repo", "--ticket=42")

	if got := w.calls("sbx", "secret", "rm"); len(got) != 0 {
		t.Errorf("unneeded secret removal: %v", got)
	}
}

func TestRemovalWhoseCleanupGotPartwayEndsClean(t *testing.T) {
	w := newWorld(t)
	w.rmExit = 1
	w.rmCleansDeepseek = true

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d, want 0\nstdout:\n%s", code, w.stdout.String())
	}
	w.assertEventOnce(`^#42 sandboxes removed`)
}

const stripeConfig = "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n\n" +
	"[repos.\"owner/repo\".secrets]\nSTRIPE_KEY = [\"api.stripe.com\", \"*.stripe.example\"] # payments\n"

func TestRepositorySecretReachesTheImplementerThroughTheProxy(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "STRIPE_KEY=" + stripeKey + "\n"
	w.config = stripeConfig

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s\nstdout:\n%s", code, w.stderr.String(), w.stdout.String())
	}

	implementer := w.sandboxes[0]
	sets := slices.DeleteFunc(w.calls("sbx", "secret", "set-custom"), func(c lightsoff.Command) bool {
		return slices.Contains(c.Args, "--help")
	})
	if len(sets) != 1 {
		t.Fatalf("set-custom calls = %v, want one, for the Implementer only", sets)
	}
	set := sets[0]
	if set.Stdin != stripeKey || !slices.Contains(set.Args, "--value-stdin") {
		t.Errorf("value not piped: %+v", set)
	}
	if flagValue(set.Args, "--sandbox") != implementer || flagValue(set.Args, "--env") != "STRIPE_KEY" {
		t.Errorf("set-custom = %v", set.Args)
	}
	var hosts []string
	for i, arg := range set.Args {
		if arg == "--host" {
			hosts = append(hosts, set.Args[i+1])
		}
	}
	if !slices.Equal(hosts, []string{"api.stripe.com", "*.stripe.example"}) {
		t.Errorf("hosts = %v", hosts)
	}

	// The kit doesn't allow the domains, so the sandbox could not reach them.
	allow := w.calls("sbx", "policy", "allow", "network")
	if len(allow) != 1 || flagValue(allow[0].Args, "--sandbox") != implementer ||
		allow[0].Args[len(allow[0].Args)-1] != "api.stripe.com,*.stripe.example" {
		t.Errorf("network rule = %v", allow)
	}

	// sbx only sets --env at creation, so an existing sandbox has to be told the
	// placeholder, which is not itself a secret.
	placeholder := flagValue(set.Args, "--placeholder")
	if placeholder == "" || strings.Contains(placeholder, stripeKey) {
		t.Fatalf("placeholder = %q", placeholder)
	}
	persisted := false
	for _, c := range w.calls("sbx", "exec") {
		line := strings.Join(c.Args, " ")
		if strings.Contains(line, "/etc/sandbox-persistent.sh") {
			persisted = slices.Contains(c.Args, implementer) && strings.Contains(line, "STRIPE_KEY="+placeholder)
		}
	}
	if !persisted {
		t.Errorf("placeholder not persisted in the sandbox: %v", w.calls("sbx", "exec"))
	}
	if w.indexOf("sbx", "exec", implementer, "dsh") < w.indexOf("sbx", "secret", "set-custom") {
		t.Errorf("the agent started before its secret arrived")
	}
	if strings.Contains(w.stdout.String()+w.stderr.String(), stripeKey) {
		t.Errorf("value in output")
	}
	for _, c := range w.commands {
		if strings.Contains(strings.Join(c.Args, " "), stripeKey) {
			t.Errorf("value in arguments: %v", c)
		}
	}
}

func TestRepositorySecretWithNoValueIsRefused(t *testing.T) {
	w := newWorld(t)
	w.config = stripeConfig

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(w.stderr.String(), "STRIPE_KEY") || !strings.Contains(w.stderr.String(), "secrets.env") {
		t.Errorf("stderr = %q", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestAnotherRepositorysSecretStaysOutOfThisRun(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "OTHER_KEY=sk-other\n"
	w.config = "[repos.\"owner/repo\"]\n\n[repos.\"owner/other\"]\n[repos.\"owner/other\".secrets]\nOTHER_KEY = [\"api.other.example\"]\n"
	w.confirm() // nobody is on this repository's allowlist

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got := w.calls("sbx", "secret", "set-custom"); len(got) != 0 {
		t.Errorf("delivered: %v", got)
	}
}

func TestOrphanedRepositorySecretIsRetriedByItsPlaceholder(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "STRIPE_KEY=" + stripeKey + "\n"
	w.config = stripeConfig
	w.rmExit = 1

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d\nstdout:\n%s", code, w.stdout.String())
	}
	if len(w.scoped[w.sandboxes[0]]) != 0 {
		t.Errorf("left behind: %v", w.scoped[w.sandboxes[0]])
	}
}

func TestSbxWithoutValueStdinIsRefusedBeforeAnySandboxWhenTheRepositoryHasSecrets(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "STRIPE_KEY=" + stripeKey + "\n"
	w.config = stripeConfig
	w.noValueStdin = true

	if code := w.run("owner/repo", "--ticket=42"); code == 0 {
		t.Errorf("exit code = 0, want non-zero")
	}
	if !strings.Contains(w.stderr.String(), "--value-stdin") || !strings.Contains(w.stderr.String(), "STRIPE_KEY") {
		t.Errorf("stderr should explain, got %q", w.stderr.String())
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		t.Errorf("sandbox created: %v", got)
	}
}

func TestSbxWithoutValueStdinIsFineWithoutRepositorySecrets(t *testing.T) {
	w := newWorld(t)
	w.noValueStdin = true

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Errorf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
}
