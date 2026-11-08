package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// impl4ReadConfig returns the config.toml a run left behind.
func impl4ReadConfig(t *testing.T, w *world) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestAllowlistAddChangesOnlyTheAllowlistLine(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.configPerm = 0o640
	w.config = "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"carol\"]   # onboarded\nimplementer_memory = \"4g\"\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice", "bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "# machines\n\n[repos.\"owner/repo\"]\nallowlist = [\"carol\", \"alice\", \"bob\"]   # onboarded\nimplementer_memory = \"4g\"\n"
	if got := impl4ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
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
	for _, part := range []string{"added", "alice", "bob"} {
		if !strings.Contains(w.stdout.String(), part) {
			t.Errorf("stdout lacks %q:\n%s", part, w.stdout.String())
		}
	}
}

func TestAllowlistAddInsertsAMissingLineAfterTheHeader(t *testing.T) {
	cases := map[string]struct{ start, want string }{
		"before other settings": {
			start: "[repos.\"owner/repo\"] # mine\nimplementer_memory = \"4g\"\n",
			want:  "[repos.\"owner/repo\"] # mine\nallowlist = [\"alice\"]\nimplementer_memory = \"4g\"\n",
		},
		"before the next table": {
			start: "[repos.\"owner/repo\"]\n\n[repos.\"owner/other\"]\nallowlist = [\"them\"]\n",
			want:  "[repos.\"owner/repo\"]\nallowlist = [\"alice\"]\n\n[repos.\"owner/other\"]\nallowlist = [\"them\"]\n",
		},
		"header is the last line": {
			start: "[repos.\"owner/repo\"]",
			want:  "[repos.\"owner/repo\"]\nallowlist = [\"alice\"]\n",
		},
		"windows endings": {
			start: "[repos.\"owner/repo\"]\r\nimplementer_memory = \"4g\"\r\n",
			want:  "[repos.\"owner/repo\"]\r\nallowlist = [\"alice\"]\r\nimplementer_memory = \"4g\"\r\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipConfig = true
			w.skipSecrets = true
			ticket2WriteConfig(t, w, tc.start, 0o600)

			if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
			if got := impl4ReadConfig(t, w); got != tc.want {
				t.Errorf("config.toml =\n%q\nwant\n%q", got, tc.want)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestAllowlistAddKeepsHandEditedEntries(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"dependabot[bot]\", \"weird name\"] # by hand\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\nallowlist = [\"dependabot[bot]\", \"weird name\", \"alice\"] # by hand\n"
	if got := impl4ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestAllowlistAddRefusesAnUnreadableConfigAndLeavesItAlone(t *testing.T) {
	const start = "[repos.\"owner/repo\"]\nallowlist = [\n\"owner\"]\n"
	w := newWorld(t)
	w.skipConfig = true
	w.skipSecrets = true
	ticket2WriteConfig(t, w, start, 0o600)

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code == 0 {
		t.Fatalf("exit code = 0, want non-zero")
	}
	if !strings.Contains(w.stderr.String(), "config.toml:2:") {
		t.Errorf("stderr should name the file and line: %q", w.stderr.String())
	}
	if got := impl4ReadConfig(t, w); got != start {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, start)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestAllowlistAddRefusesMalformedLoginsAndWritesNothing(t *testing.T) {
	bad := map[string]string{
		"bot login":        "dependabot[bot]",
		"leading hyphen":   "-alice",
		"trailing hyphen":  "alice-",
		"double hyphen":    "alice--bob",
		"underscore":       "alice_bob",
		"dot":              "alice.bob",
		"space":            "alice bob",
		"empty":            "",
		"too long":         strings.Repeat("a", 40),
		"at sign":          "alice@example.com",
		"only a hyphen":    "-",
		"slash":            "owner/alice",
		"unicode letter":   "alicé",
		"newline in login": "alice\nbob",
	}
	for name, login := range bad {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = [\"carol\"] # keep\n"

			if code := w.run("allowlist", "add", "owner/repo", "alice", login); code == 0 {
				t.Fatalf("exit code = 0, want non-zero")
			}
			if got := impl4ReadConfig(t, w); got != w.config {
				t.Errorf("config.toml changed to %q", got)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestAllowlistAddAcceptsTheEdgesOfTheLoginFormat(t *testing.T) {
	for name, login := range map[string]string{
		"one letter":    "a",
		"39 characters": "a" + strings.Repeat("b", 37) + "c",
		"hyphen inside": "alice-bob-42",
	} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/repo\"]\nallowlist = []\n"

			if code := w.run("allowlist", "add", "owner/repo", login); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
			want := "[repos.\"owner/repo\"]\nallowlist = [\"" + login + "\"]\n"
			if got := impl4ReadConfig(t, w); got != want {
				t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
			}
		})
	}
}

func TestAllowlistAddSkipsUsersAlreadyPresentIgnoringCase(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"Alice\"] # keep\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice", "bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "[repos.\"owner/repo\"]\nallowlist = [\"Alice\", \"bob\"] # keep\n"
	if got := impl4ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(strings.ToLower(w.stdout.String()), "already") {
		t.Errorf("stdout should say alice is already on the Allowlist:\n%s", w.stdout.String())
	}
}

func TestAllowlistAddWithOnlyKnownUsersSucceedsAndChangesNothing(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"Alice\"]\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, w.stderr.String())
	}
	if got := impl4ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if !strings.Contains(strings.ToLower(w.stdout.String()), "already") {
		t.Errorf("stdout should say alice is already on the Allowlist:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestAllowlistAddRefusesARepositoryThatIsNotOptedIn(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

	if code := w.run("allowlist", "add", "owner/repo", "alice"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	for _, part := range []string{"not opted in", "lights-off repo add owner/repo"} {
		if !strings.Contains(w.stderr.String(), part) {
			t.Errorf("stderr lacks %q:\n%s", part, w.stderr.String())
		}
	}
	if got := impl4ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml changed to %q", got)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoAddWithAllowOptsInWithUsersAndSkipsTheEmptyWarning(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/other\"]\nallowlist = [\"someone\"]\n"

	if code := w.run("repo", "add", "owner/repo", "--allow=alice,bob"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := w.config + "\n[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"bob\"]\n"
	if got := impl4ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(w.stderr.String(), "--n will pick up nothing") {
		t.Errorf("stderr should not warn about an empty Allowlist:\n%s", w.stderr.String())
	}
	if !strings.Contains(w.stdout.String(), "opted in") {
		t.Errorf("stdout should say the repository is opted in:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoAddWithAllowOnAnOptedInRepositoryAddsOnlyMissingUsers(t *testing.T) {
	t.Run("adds a missing user", func(t *testing.T) {
		w := newWorld(t)
		w.skipSecrets = true
		w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\"] # keep\n"

		if code := w.run("repo", "add", "owner/repo", "--allow=carol"); code != 0 {
			t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
		}
		want := "[repos.\"owner/repo\"]\nallowlist = [\"alice\", \"carol\"] # keep\n"
		if got := impl4ReadConfig(t, w); got != want {
			t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
		}
		for _, part := range []string{"already opted in", "carol"} {
			if !strings.Contains(w.stdout.String(), part) {
				t.Errorf("stdout lacks %q:\n%s", part, w.stdout.String())
			}
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})

	t.Run("no allowlist line yet", func(t *testing.T) {
		w := newWorld(t)
		w.skipSecrets = true
		w.config = "[repos.\"owner/repo\"] # mine\nimplementer_memory = \"4g\"\n"

		if code := w.run("repo", "add", "owner/repo", "--allow=carol"); code != 0 {
			t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
		}
		want := "[repos.\"owner/repo\"] # mine\nallowlist = [\"carol\"]\nimplementer_memory = \"4g\"\n"
		if got := impl4ReadConfig(t, w); got != want {
			t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
		}
		for _, part := range []string{"already opted in", "carol"} {
			if !strings.Contains(w.stdout.String(), part) {
				t.Errorf("stdout lacks %q:\n%s", part, w.stdout.String())
			}
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})

	t.Run("all users are already present", func(t *testing.T) {
		w := newWorld(t)
		w.skipSecrets = true
		w.config = "[repos.\"owner/repo\"]\nallowlist = [\"alice\"] # keep\n"

		if code := w.run("repo", "add", "owner/repo", "--allow=ALICE"); code != 0 {
			t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, w.stderr.String())
		}
		if got := impl4ReadConfig(t, w); got != w.config {
			t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
		}
		for _, part := range []string{"already opted in", "already on the Allowlist"} {
			if !strings.Contains(w.stdout.String(), part) {
				t.Errorf("stdout lacks %q:\n%s", part, w.stdout.String())
			}
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})
}

func TestRepoAddWithAllowRefusesMalformedLoginsAndWritesNothing(t *testing.T) {
	t.Run("nothing to change", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "lights-off")
		w := newWorld(t)
		w.configDir = dir
		w.skipConfig = true
		w.skipSecrets = true

		if code := w.run("repo", "add", "owner/repo", "--allow=alice,bad_login"); code == 0 {
			t.Fatalf("exit code = 0, want non-zero")
		}
		if _, err := os.Stat(filepath.Join(dir, "config.toml")); !os.IsNotExist(err) {
			t.Errorf("config.toml should not exist, stat err = %v", err)
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})

	t.Run("existing file", func(t *testing.T) {
		w := newWorld(t)
		w.skipSecrets = true
		w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

		if code := w.run("repo", "add", "owner/repo", "--allow=alice,dependabot[bot]"); code == 0 {
			t.Fatalf("exit code = 0, want non-zero")
		}
		if got := impl4ReadConfig(t, w); got != w.config {
			t.Errorf("config.toml changed to %q", got)
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})
}

func TestConfigCommandsWrongArgumentsPrintUsage(t *testing.T) {
	cases := map[string][]string{
		"allowlist alone":        {"allowlist"},
		"allowlist with no repo": {"allowlist", "add"},
		"allowlist with no user": {"allowlist", "add", "owner/repo"},
		"allowlist unknown":      {"allowlist", "remove", "owner/repo", "alice"},
		"allowlist run flag":     {"allowlist", "add", "owner/repo", "alice", "--n=1"},
		"allowlist allow flag":   {"allowlist", "add", "owner/repo", "alice", "--allow=bob"},
		"allowlist bad repo":     {"allowlist", "add", "owner", "alice"},
		"repo add extra":         {"repo", "add", "owner/repo", "alice"},
		"repo add empty allow":   {"repo", "add", "owner/repo", "--allow="},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

			if code := w.run(args...); code != 2 {
				t.Fatalf("exit code = %d, want 2\nstderr:\n%s", code, w.stderr.String())
			}
			if !strings.Contains(w.stderr.String(), "usage:") || !strings.Contains(w.stderr.String(), "allowlist add") {
				t.Errorf("stderr should print usage naming allowlist add: %q", w.stderr.String())
			}
			if got := impl4ReadConfig(t, w); got != w.config {
				t.Errorf("config.toml changed to %q", got)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestHelpAndRefusalNameTheAllowlistCommands(t *testing.T) {
	t.Run("help", func(t *testing.T) {
		w := newWorld(t)
		if code := w.run("--help"); code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
		for _, part := range []string{"lights-off allowlist add <owner>/<repo> <user>", "--allow=<user>"} {
			if !strings.Contains(w.stderr.String(), part) {
				t.Errorf("help lacks %q:\n%s", part, w.stderr.String())
			}
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})

	t.Run("not opted in refusal", func(t *testing.T) {
		w := newWorld(t)
		w.config = "[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"

		if code := w.run("owner/repo", "--ticket=42"); code == 0 {
			t.Fatalf("exit code = 0, want non-zero")
		}
		want := "lights-off repo add owner/repo --allow=<your-github-username>"
		if !strings.Contains(w.stderr.String(), want) {
			t.Errorf("stderr should contain %q:\n%s", want, w.stderr.String())
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})
}
