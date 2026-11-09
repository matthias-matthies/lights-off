package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTicket5AllowlistRemoveRemovesUsersAndKeepsTheTrailingComment(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"owner\", \"alice\", \"bob\"] # who counts\nimplementer_memory = \"4g\"\n"

	if code := w.run("allowlist", "remove", "owner/repo", "alice", "bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # who counts\nimplementer_memory = \"4g\"\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	for _, user := range []string{"alice", "bob"} {
		if !strings.Contains(w.stdout.String(), user) {
			t.Errorf("stdout should name %s:\n%s", user, w.stdout.String())
		}
	}
	if strings.Contains(w.stderr.String(), "pick up nothing") {
		t.Errorf("no empty-Allowlist warning expected:\n%s", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket5AllowlistRemoveMatchesIgnoringCase(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"Bob\"]\n"

	if code := w.run("allowlist", "remove", "owner/repo", "Alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\nallowlist = [\"Bob\"]\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket5AllowlistRemoveOfAMissingUserFailsNamingThemAndWritesNothing(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"bob\"] # keep\n"

	if code := w.run("allowlist", "remove", "owner/repo", "alice", "carol"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stderr.String(), "carol") {
		t.Errorf("stderr should name carol:\n%s", w.stderr.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket5AllowlistRemoveRefusesEveryBadNameBeforeWriting(t *testing.T) {
	cases := map[string]string{
		"bot login":      "dependabot[bot]",
		"leading hyphen": "-alice",
		"double hyphen":  "alice--bob",
		"underscore":     "alice_bob",
		"empty":          "",
		"too long":       strings.Repeat("a", 40),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"bob\"] # keep\n"

			if code := w.run("allowlist", "remove", "owner/repo", "alice", bad); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, w.stderr.String())
			}
			if got := ticket2ReadConfig(t, w); got != w.config {
				t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestTicket5AllowlistRemoveOnARepositoryNotOptedInFailsAndPointsToRepoAdd(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"alice\"]\n"

	if code := w.run("allowlist", "remove", "owner/repo", "alice"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo add owner/repo") {
		t.Errorf("stderr should point to repo add:\n%s", w.stderr.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket5AllowlistRemoveOfTheLastUserLeavesAnEmptyAllowlistAndWarns(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\"] # who counts\n"

	if code := w.run("allowlist", "remove", "owner/repo", "ALICE"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\nallowlist = [] # who counts\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	for _, s := range []string{"--n will pick up nothing", "--ticket still works with a confirmed override"} {
		if !strings.Contains(w.stderr.String(), s) {
			t.Errorf("stderr should warn %q:\n%s", s, w.stderr.String())
		}
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}

	// The emptied file is still one lights-off reads: the repository stays opted in.
	w.skipConfig = true
	w.stdout.Reset()
	w.stderr.Reset()
	if code := w.run("repo", "list"); code != 0 {
		t.Fatalf("repo list exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stdout.String(), "owner/repo: (empty allowlist)") {
		t.Errorf("repo list should show the empty Allowlist:\n%s", w.stdout.String())
	}
}

func TestTicket5AllowlistRemoveUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"no users":       {"allowlist", "remove", "owner/repo"},
		"no repository":  {"allowlist", "remove"},
		"bad repository": {"allowlist", "remove", "owner", "alice"},
		"with n":         {"allowlist", "remove", "owner/repo", "alice", "--n=5"},
		"with ticket":    {"allowlist", "remove", "owner/repo", "alice", "--ticket=42"},
		"with allow":     {"allowlist", "remove", "owner/repo", "alice", "--allow=bob"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\"]\n"

			if code := w.run(args...); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, w.stderr.String())
			}
			if !strings.Contains(w.stderr.String(), "usage:") {
				t.Errorf("stderr should print usage: %q", w.stderr.String())
			}
			if got := ticket2ReadConfig(t, w); got != w.config {
				t.Errorf("config.toml changed to %q", got)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestTicket5HelpCoversAllowlistRemove(t *testing.T) {
	w := newWorld(t)
	if code := w.run("--help"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(w.stderr.String(), "lights-off allowlist remove <owner>/<repo> <user>...") {
		t.Errorf("help should cover allowlist remove:\n%s", w.stderr.String())
	}
}

func TestTicket5AllowlistRemoveKeepsTheFileMode(t *testing.T) {
	w := newWorld(t)
	w.configDir = t.TempDir()
	w.skipConfig = true
	w.skipSecrets = true
	ticket2WriteConfig(t, w, "[repos.\"owner/repo\"]\nallowlist = [\"owner\", \"alice\"]\n", 0o640)

	if code := w.run("allowlist", "remove", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	info, err := os.Stat(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("config.toml mode = %04o, want 0640", mode)
	}
	if got, want := ticket2ReadConfig(t, w), "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n"; got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}
