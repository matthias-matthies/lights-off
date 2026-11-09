package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoRemoveRemovesTheTablesAndPrintsThem(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.configPerm = 0o640
	w.config = "# machines\n\n" +
		"[repos.\"owner/other\"]\nallowlist = [\"someone\"] # keep me\n\n" +
		"[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # me\n\n" +
		"[repos.\"owner/repo\".secrets]\nSTRIPE_KEY = [\"api.stripe.com\"]\n\n" +
		"[repos.\"owner/last\"]\nallowlist = []\n"

	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	want := "# machines\n\n" +
		"[repos.\"owner/other\"]\nallowlist = [\"someone\"] # keep me\n\n" +
		"[repos.\"owner/last\"]\nallowlist = []\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	removed := "[repos.\"owner/repo\"]\nallowlist = [\"owner\"] # me\n\n" +
		"[repos.\"owner/repo\".secrets]\nSTRIPE_KEY = [\"api.stripe.com\"]\n\n"
	if !strings.Contains(w.stdout.String(), removed) {
		t.Errorf("stdout should print the removed lines:\n%s", w.stdout.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
	info, err := os.Stat(filepath.Join(w.configDir, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o640 {
		t.Errorf("config.toml mode = %04o, want 0640", mode)
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

func TestRepoRemoveOnlyNamesSecretsNoRemainingRepositoryDeclares(t *testing.T) {
	w := newWorld(t)
	w.secrets = "UNRELATED_VALUE=whatever\n"
	w.secretsPerm = 0o644
	w.config = "[repos.\"owner/repo\"]\nallowlist = []\n\n" +
		"[repos.\"owner/repo\".secrets]\nSTRIPE_KEY = [\"api.stripe.com\"]\nSHARED_KEY = [\"shared.example.com\"]\n\n" +
		"[repos.\"owner/other\"]\nallowlist = []\n\n" +
		"[repos.\"owner/other\".secrets]\nSHARED_KEY = [\"shared.example.com\"]\n"

	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}

	gotConfig := ticket2ReadConfig(t, w)
	wantConfig := "[repos.\"owner/other\"]\nallowlist = []\n\n" +
		"[repos.\"owner/other\".secrets]\nSHARED_KEY = [\"shared.example.com\"]\n"
	if gotConfig != wantConfig {
		t.Errorf("config.toml =\n%q\nwant\n%q", gotConfig, wantConfig)
	}
	for _, part := range []string{"STRIPE_KEY", "secrets.env", "delete", "no remaining Target Repository declares"} {
		if !strings.Contains(w.stderr.String(), part) {
			t.Errorf("stderr lacks %q:\n%s", part, w.stderr.String())
		}
	}
	if strings.Contains(w.stderr.String(), "SHARED_KEY") {
		t.Errorf("stderr should not name a secret another Target Repository still declares:\n%s", w.stderr.String())
	}
	// An undeclared value and a mode lights-off would refuse at preflight are
	// both left untouched: the command never reads or writes secrets.env.
	data, err := os.ReadFile(filepath.Join(w.configDir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != w.secrets {
		t.Errorf("secrets.env = %q, want it unchanged: %q", got, w.secrets)
	}
	info, err := os.Stat(filepath.Join(w.configDir, "secrets.env"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o644 {
		t.Errorf("secrets.env mode = %04o, want 0644", mode)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoRemoveNotOptedInFailsAndLeavesTheFile(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# keep\n\n[repos.\"owner/other\"]\nallowlist = []\n"

	if code := w.run("repo", "remove", "owner/repo"); code != 1 {
		t.Fatalf("exit code = %d, want 1\nstderr:\n%s", code, w.stderr.String())
	}
	if !strings.Contains(w.stderr.String(), "not opted in") {
		t.Errorf("stderr should say the repository is not opted in: %q", w.stderr.String())
	}
	if got := ticket2ReadConfig(t, w); got != w.config {
		t.Errorf("config.toml =\n%q\nwant it unchanged:\n%q", got, w.config)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoRemoveRefusesAnUnreadableConfigAndLeavesItAlone(t *testing.T) {
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
			w.skipSecrets = true
			w.config = text

			if code := w.run("repo", "remove", "owner/repo"); code == 0 {
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

func TestRepoRemoveThenTicketRunIsRefusedAsNotOptedIn(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n\n" +
		"[repos.\"owner/other\"]\nallowlist = []\n"
	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("repo remove exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	optedOut := ticket2ReadConfig(t, w)

	next := newWorld(t)
	next.skipSecrets = true
	next.config = optedOut
	if code := next.run("owner/repo", "--ticket=42"); code != 1 {
		t.Fatalf("ticket run exit code = %d, want 1\nstderr:\n%s", code, next.stderr.String())
	}
	for _, part := range []string{"not opted in", "repo add owner/repo"} {
		if !strings.Contains(next.stderr.String(), part) {
			t.Errorf("stderr lacks %q:\n%s", part, next.stderr.String())
		}
	}
	if len(next.commands) != 0 {
		t.Errorf("commands sent: %v", next.commands)
	}
}

func TestRepoRemoveLeavesTheCacheAndRunLogsAlone(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/repo\"]\nallowlist = []\n"
	checkout := filepath.Join(w.cacheDir, "owner", "repo", "checkout")
	logs := filepath.Join(w.stateDir, "owner-repo", "run.log")
	for path, content := range map[string]string{checkout: "checkout", logs: "run log"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	for path, want := range map[string]string{checkout: "checkout", logs: "run log"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := string(data); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoRemoveKeepsWindowsLineEndings(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "# keep\r\n\r\n" +
		"[repos.\"owner/other\"]\r\nallowlist = []\r\n\r\n" +
		"[repos.\"owner/repo\"]\r\nallowlist = []\r\n\r\n" +
		"[repos.\"owner/repo\".secrets]\r\nSTRIPE_KEY = [\"api.stripe.com\"]\r\n\r\n" +
		"[repos.\"owner/last\"]\r\nallowlist = []\r\n"

	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "# keep\r\n\r\n" +
		"[repos.\"owner/other\"]\r\nallowlist = []\r\n\r\n" +
		"[repos.\"owner/last\"]\r\nallowlist = []\r\n"
	if got := ticket2ReadConfig(t, w); got != want {
		t.Errorf("config.toml =\n%q\nwant\n%q", got, want)
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoRemoveWrongArgumentsPrintUsage(t *testing.T) {
	cases := map[string][]string{
		"no repository":        {"repo", "remove"},
		"extra positional":     {"repo", "remove", "owner/repo", "extra"},
		"malformed repository": {"repo", "remove", "owner"},
		"ticket flag":          {"repo", "remove", "owner/repo", "--ticket=42"},
		"n flag":               {"repo", "remove", "owner/repo", "--n=5"},
		"no-dashboard flag":    {"repo", "remove", "owner/repo", "--no-dashboard"},
		"unknown repo command": {"repo", "enable", "owner/repo"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = "[repos.\"owner/other\"]\nallowlist = []\n"

			if code := w.run(args...); code != 2 {
				t.Fatalf("exit code = %d, want 2; stderr:\n%s", code, w.stderr.String())
			}
			if !strings.Contains(w.stderr.String(), "usage:") || !strings.Contains(w.stderr.String(), "repo remove") {
				t.Errorf("stderr should print usage naming repo remove: %q", w.stderr.String())
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

func TestRepoRemoveHelpListsRepoRemoveAsOptingOut(t *testing.T) {
	w := newWorld(t)
	if code := w.run("--help"); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(w.stderr.String(), "lights-off repo remove <owner>/<repo>") ||
		!strings.Contains(w.stderr.String(), "opt out") {
		t.Errorf("help should list repo remove as opting out:\n%s", w.stderr.String())
	}
	if len(w.commands) != 0 {
		t.Errorf("commands sent: %v", w.commands)
	}
}

func TestRepoRemoveKeepsTheCommentAboveTheNextTable(t *testing.T) {
	cases := map[string]struct{ config, want, removed string }{
		"next repository": {
			config: "[repos.\"owner/repo\"]\nallowlist = [\"a\"]\n\n# Bob's team\n[repos.\"owner/next\"]\nallowlist = [\"bob\"]\n",
			want:   "# Bob's team\n[repos.\"owner/next\"]\nallowlist = [\"bob\"]\n",
		},
		"after the secrets table": {
			config: "[repos.\"owner/first\"]\nallowlist = []\n\n[repos.\"owner/repo\"]\nallowlist = []\n# its secrets\n[repos.\"owner/repo\".secrets]\nX = [\"x.com\"]\n\n# Bob's team\n# since October\n[repos.\"owner/next\"]\nallowlist = []\n",
			want:   "[repos.\"owner/first\"]\nallowlist = []\n\n# Bob's team\n# since October\n[repos.\"owner/next\"]\nallowlist = []\n",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.skipSecrets = true
			w.config = c.config

			if code := w.run("repo", "remove", "owner/repo"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
			if got := ticket2ReadConfig(t, w); got != c.want {
				t.Errorf("config.toml =\n%q\nwant\n%q", got, c.want)
			}
			if strings.Contains(w.stdout.String(), "Bob's team") {
				t.Errorf("printed the next table's comment as removed:\n%s", w.stdout.String())
			}
		})
	}
}

func TestRepoRemoveOfTheLastTableLeavesNoTrailingBlankLine(t *testing.T) {
	w := newWorld(t)
	w.skipSecrets = true
	w.config = "[repos.\"owner/first\"]\nallowlist = [\"a\"]\n\n[repos.\"owner/repo\"]\nallowlist = [\"b\"]\n"

	if code := w.run("repo", "remove", "owner/repo"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	if got, want := ticket2ReadConfig(t, w), "[repos.\"owner/first\"]\nallowlist = [\"a\"]\n"; got != want {
		t.Errorf("config.toml = %q, want %q", got, want)
	}
}
