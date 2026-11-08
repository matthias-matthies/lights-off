package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ticket2ReadConfig reads the config.toml the run left behind.
func ticket2ReadConfig(t *testing.T, w *world) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// ticket2WriteConfig writes a starting config.toml without going through the
// world, which normally rewrites it before every run.
func ticket2WriteConfig(t *testing.T, w *world, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(w.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(w.configDir, "config.toml")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestTicket2RepoAddOptsInAndKeepsTheFile(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/other\"]\nallowlist = [\"someone\"] # keep me\n"

	if code := w.run("repo", "add", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := w.config + "\n[repos.\"owner/repo\"]\nallowlist = []\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
	for _, part := range []string{"--n", "Allowlist", "--ticket", "confirmed override"} {
		if !strings.Contains(w.stderr.String(), part) {
			t.Errorf("warning lacks %q:\n%s", part, w.stderr.String())
		}
	}
	if !strings.Contains(w.stdout.String(), "opted in") {
		t.Errorf("stdout should say the repository is opted in:\n%s", w.stdout.String())
	}
	entries, err := os.ReadDir(w.configDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".config.toml.tmp-") {
			t.Errorf("write left a temporary file: %s", entry.Name())
		}
	}
}

func TestTicket2RepoAddAppendsAtTheEnd(t *testing.T) {
	cases := map[string]string{
		"empty file":            "",
		"no trailing newline":   "[repos.\"owner/other\"]\nallowlist = [\"someone\"]",
		"one trailing newline":  "[repos.\"owner/other\"]\nallowlist = [\"someone\"]\n",
		"two trailing newlines": "[repos.\"owner/other\"]\nallowlist = [\"someone\"]\n\n",
		"windows endings":       "[repos.\"owner/other\"]\r\nallowlist = [\"someone\"]\r\n",
	}
	for name, start := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.skipConfig = true
			ticket2WriteConfig(t, w, start, 0o600)

			if code := w.run("repo", "add", "owner/repo"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}

			sep := "\n"
			if strings.Contains(start, "\r\n") {
				sep = "\r\n"
			}
			want := start
			switch {
			case start == "":
			case strings.HasSuffix(start, sep+sep):
			case strings.HasSuffix(start, sep):
				want += sep
			default:
				want += sep + sep
			}
			want += "[repos.\"owner/repo\"]" + sep + "allowlist = []" + sep
			if got := ticket2ReadConfig(t, w); got != want {
				t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestTicket2RepoAddCreatesMissingDirectoryAndFile(t *testing.T) {
	t.Run("missing directory", func(t *testing.T) {
		w := newWorld(t)
		w.configDir = filepath.Join(t.TempDir(), "lights-off")
		w.skipConfig = true
		w.skipSecrets = true

		if code := w.run("repo", "add", "owner/repo"); code != 0 {
			t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
		}
		info, err := os.Stat(w.configDir)
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("config directory mode = %04o, want 0700", mode)
		}
		info, err = os.Stat(filepath.Join(w.configDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("config.toml mode = %04o, want 0600", mode)
		}
		want := "[repos.\"owner/repo\"]\nallowlist = []\n"
		if got := ticket2ReadConfig(t, w); got != want {
			t.Errorf("config.toml = %q, want %q", got, want)
		}
		if len(w.commands) != 0 {
			t.Errorf("commands sent: %v", w.commands)
		}
	})

	t.Run("existing directory, missing file", func(t *testing.T) {
		w := newWorld(t)
		w.configDir = t.TempDir()
		w.skipConfig = true
		w.skipSecrets = true

		if code := w.run("repo", "add", "owner/repo"); code != 0 {
			t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
		}
		info, err := os.Stat(filepath.Join(w.configDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("config.toml mode = %04o, want 0600", mode)
		}
	})
}

func TestTicket2RepoAddKeepsAnExistingMode(t *testing.T) {
	w := newWorld(t)
	w.configDir = t.TempDir()
	w.skipConfig = true
	w.skipSecrets = true
	ticket2WriteConfig(t, w, "# keep\n[repos.\"owner/other\"]\nallowlist = []\n", 0o640)

	if code := w.run("repo", "add", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	info, err := os.Stat(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("config.toml mode = %04o, want 0640", mode)
	}
	if !strings.Contains(ticket2ReadConfig(t, w), "# keep") {
		t.Errorf("config.toml lost its comment")
	}
}

func TestTicket2RepoAddAlreadyOptedIn(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# mine\n\n[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # keep\n"

	if code := w.run("repo", "add", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stdout.String(), "already opted in") {
		t.Errorf("stdout should say already opted in:\n%s", w.stdout.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket2RepoAddRefusesAnUnreadableConfigAndLeavesItAlone(t *testing.T) {
	cases := map[string]string{
		"unclosed table":      "\n[repos.\"owner/repo\"\n",
		"unknown key":         "[repos.\"owner/repo\"]\ncolour = \"red\"\n",
		"multi-line array":    "[repos.\"owner/repo\"]\nallowlist = [\n\"owner\"]\n",
		"trailing junk":       "[repos.\"owner/repo\"]\nallowlist = [\"owner\"] x\n",
		"repeated table":      "[repos.\"owner/repo\"]\n[repos.\"owner/repo\"]\n",
		"key outside a table": "\nallowlist = [\"owner\"]\n",
	}
	for name, text := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.config = text
			w.skipSecrets = true

			if code := w.run("repo", "add", "owner/repo"); code == 0 {
				t.Fatalf("exit code = 0, want non-zero")
			}
			if !strings.Contains(w.stderr.String(), "config.toml:") {
				t.Errorf("stderr should name the file and line: %q", w.stderr.String())
			}
			if got := ticket2ReadConfig(t, w); got != text {
				t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, text)
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestTicket2RepoAddRefusesAMalformedRepository(t *testing.T) {
	for _, repo := range []string{"owner", "owner/repo/extra", "owner//repo", "-owner/repo", "owner/re po"} {
		t.Run(repo, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

			if code := w.run("repo", "add", repo); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, w.stderr.String())
			}
			if !strings.Contains(w.stderr.String(), "usage:") || !strings.Contains(w.stderr.String(), "repo add") {
				t.Errorf("stderr should print usage naming repo add: %q", w.stderr.String())
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

func TestTicket2RepoRefusesRunFlags(t *testing.T) {
	cases := map[string][]string{
		"n":            {"repo", "add", "owner/repo", "--n=5"},
		"ticket":       {"repo", "add", "owner/repo", "--ticket=42"},
		"no-dashboard": {"repo", "add", "owner/repo", "--no-dashboard"},
		"version":      {"repo", "add", "owner/repo", "--version"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

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

func TestTicket2RepoWrongArgumentsPrintUsage(t *testing.T) {
	cases := map[string][]string{
		"no subcommand":    {"repo"},
		"no repository":    {"repo", "add"},
		"unknown":          {"repo", "remove", "owner/repo"},
		"extra positional": {"repo", "add", "owner/repo", "extra"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

			if code := w.run(args...); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, w.stderr.String())
			}
			if !strings.Contains(w.stderr.String(), "usage:") || !strings.Contains(w.stderr.String(), "repo add") {
				t.Errorf("stderr should print usage naming repo add: %q", w.stderr.String())
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

func TestTicket2HelpListsRepoAddAsOptingIn(t *testing.T) {
	w := newWorld(t)
	if code := w.run("--help"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo add <owner>/<repo>") ||
		!strings.Contains(w.stderr.String(), "opt in") {
		t.Errorf("help should list repo add as opting in:\n%s", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestTicket2RepoAddThenTicketRunGetsPastTheOptInCheck(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# machines\n\n[repos.\"owner/other\"]\nallowlist = [\"owner\"]\n"
	if code := w.run("repo", "add", "owner/repo"); code != 0 {
		t.Fatalf("repo add exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	optedIn := ticket2ReadConfig(t, w)

	next := newWorld(t)
	next.config = optedIn
	next.confirm() // the empty Allowlist needs an override, as the warning says
	if code := next.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("ticket run exit code = %d\nstderr:\n%s", code, next.stderr.String())
	}
	if strings.Contains(next.stderr.String(), "not opted in") || strings.Contains(next.stderr.String(), "repo add") {
		t.Errorf("ticket run was still refused as not opted in:\n%s", next.stderr.String())
	}
	if len(next.commands) == 0 {
		t.Error("ticket run stopped before doing anything")
	}
}
