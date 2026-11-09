package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTicket4AllowlistAddAddsUsersAndKeepsTheTrailingComment(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # who counts\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice", "bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"owner\", \"alice\", \"bob\"] # who counts\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	for _, user := range []string{"alice", "bob"} {
		if !strings.Contains(w.stdout.String(), user) {
			t.Errorf("stdout should name %s:\n%s", user, w.stdout.String())
		}
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddInsertsALineAfterTheHeader(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"] # mine\nimplementer_memory = \"4g\"\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "[repos.\"owner/repo\"] # mine\nallowlist = [\"alice\"]\nimplementer_memory = \"4g\"\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(w.stdout.String(), "alice") {
		t.Errorf("stdout should name alice:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddAlreadyThereSucceedsWithoutWriting(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"Alice\", \"bob\"] # keep\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice", "BOB"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if count := strings.Count(strings.ToLower(w.stdout.String()), "already"); count < 2 {
		t.Errorf("stdout should say both users are already on the Allowlist:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddAddsOnlyMissingUsersInTheTypedCase(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"Alice\"]\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice", "Bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\nallowlist = [\"Alice\", \"Bob\"]\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(strings.ToLower(w.stdout.String()), "alice") ||
		!strings.Contains(w.stdout.String(), "Bob") {
		t.Errorf("stdout should name the duplicate and the addition:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddRefusesEveryBadNameBeforeWriting(t *testing.T) {
	cases := map[string]string{
		"bot login":          "dependabot[bot]",
		"leading hyphen":     "-alice",
		"trailing hyphen":    "alice-",
		"double hyphen":      "alice--bob",
		"underscore":         "alice_bob",
		"space":              "alice bob",
		"empty":              "",
		"too long":           strings.Repeat("a", 40),
		"dot":                "alice.bob",
		"forty after a dash": "a-" + strings.Repeat("b", 39),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # keep\n"

			if code := w.run("allowlist", "add", "owner/repo", "alice", bad); code == 0 {
				t.Fatalf("exit code = 0, want non-zero")
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

func TestTicket4AllowlistAddOnARepositoryNotOptedInFailsAndPointsToRepoAdd(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo add owner/repo --allow=<your-github-username>") {
		t.Errorf("stderr should point to repo add with --allow:\n%s", w.stderr.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RepoAddWithAllowOptsInWithUsersAndNoWarning(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/other\"]\nallowlist = [\"someone\"]\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=alice,bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := w.config + "\n[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"bob\"]\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if w.stderr.Len() != 0 {
		t.Errorf("stderr should be empty, got %q", w.stderr.String())
	}
	for _, part := range []string{"opted in", "alice", "bob"} {
		if !strings.Contains(w.stdout.String(), part) {
			t.Errorf("stdout should contain %q:\n%s", part, w.stdout.String())
		}
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RepoAddWithAllowOnAnOptedInRepositoryAddsTheMissingUsers(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# mine\n\n[repos.\"owner/repo\"]\nallowlist = [\"alice\"] # keep\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=carol,Alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "# mine\n\n[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"carol\"] # keep\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(w.stdout.String(), "already opted in") {
		t.Errorf("stdout should say already opted in:\n%s", w.stdout.String())
	}
	if !strings.Contains(w.stdout.String(), "carol") {
		t.Errorf("stdout should say carol was added:\n%s", w.stdout.String())
	}
	if !strings.Contains(strings.ToLower(w.stdout.String()), "alice") ||
		!strings.Contains(strings.ToLower(w.stdout.String()), "already on") {
		t.Errorf("stdout should say alice was already there:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RepoAddWithAllowAddsALineToARepositoryWithoutOne(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"] # mine\nimplementer_memory = \"4g\"\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=carol"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "[repos.\"owner/repo\"] # mine\nallowlist = [\"carol\"]\nimplementer_memory = \"4g\"\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(w.stdout.String(), "already opted in") ||
		!strings.Contains(w.stdout.String(), "carol") {
		t.Errorf("stdout should say already opted in and what it added:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RepoAddWithAllowRefusesEveryBadNameBeforeWriting(t *testing.T) {
	cases := map[string]string{
		"bot login":       "dependabot[bot]",
		"leading hyphen":  "-alice",
		"trailing hyphen": "alice-",
		"double hyphen":   "alice--bob",
		"underscore":      "alice_bob",
		"empty part":      "alice,,bob",
		"space":           "alice bob",
		"too long":        strings.Repeat("a", 40),
		"dot":             "alice.bob",
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

			if code := w.run("repo", "add", "owner/repo", "--allow="+bad); code == 0 {
				t.Fatalf("exit code = 0, want non-zero")
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

func TestTicket4RepoAddWithAllowAllOrNothingOnAnOptedInRepository(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # keep\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=alice,dependabot[bot]"); code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RunRefusalNamesRepoAddWithAllow(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

	if code := w.run("owner/repo", "--ticket=42"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo add owner/repo --allow=<your-github-username>") {
		t.Errorf("refusal should point to repo add with --allow:\n%s", w.stderr.String())
	}
	if strings.Contains(w.stderr.String(), `[repos."owner/repo"]`) {
		t.Errorf("refusal should not print config lines:\n%s", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddReadsAWindowsConfig(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\r\nallowlist = [\"owner\"] # mine\r\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\r\nallowlist = [\"owner\", \"alice\"] # mine\r\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddInsertsAWindowsLine(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\r\nimplementer_memory = \"4g\"\r\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\r\nallowlist = [\"alice\"]\r\nimplementer_memory = \"4g\"\r\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistCommandsRejectRunFlagsAndUsageErrors(t *testing.T) {
	cases := map[string][]string{
		"allowlist with no users":        {"allowlist", "add", "owner/repo"},
		"allowlist with no repository":   {"allowlist", "add"},
		"allowlist unknown subcommand":   {"allowlist", "rename", "owner/repo", "alice"},
		"allowlist with n":               {"allowlist", "add", "owner/repo", "alice", "--n=5"},
		"allowlist with ticket":          {"allowlist", "add", "owner/repo", "alice", "--ticket=42"},
		"allowlist with no-dashboard":    {"allowlist", "add", "owner/repo", "alice", "--no-dashboard"},
		"allowlist with version":         {"allowlist", "add", "owner/repo", "alice", "--version"},
		"allowlist with allow":           {"allowlist", "add", "owner/repo", "alice", "--allow=bob"},
		"repo list with allow":           {"repo", "list", "--allow=alice"},
		"repo add with extra positional": {"repo", "add", "owner/repo", "alice"},
		"run with allow":                 {"owner/repo", "--ticket=42", "--allow=alice"},
		"plan with allow":                {"plan", "owner/repo", "--allow=alice"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n"

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

func TestTicket4HelpAndUsageCoverAllowlistAddAndAllow(t *testing.T) {
	w := newWorld(t)
	if code := w.run("--help"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	for _, want := range []string{"lights-off allowlist add <owner>/<repo> <user>...", "--allow=<user>,..."} {
		if !strings.Contains(w.stderr.String(), want) {
			t.Errorf("help should contain %q:\n%s", want, w.stderr.String())
		}
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4AllowlistAddKeepsTheFileMode(t *testing.T) {
	w := newWorld(t)
	w.configDir = t.TempDir()
	w.skipConfig = true
	w.skipSecrets = true
	text := "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n"
	ticket2WriteConfig(t, w, text, 0o640)

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	info, err := os.Stat(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("config.toml mode = %04o, want 0640", mode)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket4RepoAddWithAllowKeepsOneCaseInsensitiveCopy(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"someone\"]\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=alice,Alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := w.config + "\n[repos.\"owner/repo\"]\nallowlist = [\"alice\"]\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(strings.ToLower(w.stdout.String()), "alice") {
		t.Errorf("stdout should name alice:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}
