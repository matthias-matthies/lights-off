package lightsoff_test

import (
	"strings"
	"testing"
)

func TestConfigThatOptsTheRepositoryIn(t *testing.T) {
	cases := map[string]string{
		"plain":            "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n",
		"no allowlist yet": "[repos.\"owner/repo\"]\n",
		"comments":         "# machines\n\n[repos.\"owner/repo\"] # mine\nallowlist = [\"owner\"] # me\n",
		"several":          "[repos.\"owner/other\"]\nallowlist = [\"a\", \"b\",]\n\n[repos.\"owner/repo\"]\nallowlist = []\n",
		"hash in a string": "[repos.\"owner/repo\"]\nallowlist = [\"a#b\"]\n",
		"windows endings":  "[repos.\"owner/repo\"]\r\nallowlist = [\"owner\"]\r\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.config = config
			w.confirm() // some allow nobody, which a person can override
			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}
		})
	}
}

func TestUnreadableConfigIsRefusedNamingTheLine(t *testing.T) {
	cases := map[string]string{
		"unclosed table":      "\n[repos.\"owner/repo\"\n",
		"unquoted repository": "\n[repos.owner/repo]\n",
		"other table":         "\n[settings]\n",
		"key outside a table": "\nallowlist = [\"owner\"]\n",
		"unknown key":         "[repos.\"owner/repo\"]\ncolour = \"red\"\n",
		"not an array":        "[repos.\"owner/repo\"]\nallowlist = \"owner\"\n",
		"multi-line array":    "[repos.\"owner/repo\"]\nallowlist = [\n\"owner\"]\n",
		"unclosed string":     "[repos.\"owner/repo\"]\nallowlist = [\"owner]\n",
		"escape in a string":  "[repos.\"owner/repo\"]\nallowlist = [\"o\\\"wner\"]\n",
		"trailing junk":       "[repos.\"owner/repo\"]\nallowlist = [\"owner\"] x\n",
		"repeated table":      "[repos.\"owner/repo\"]\n[repos.\"owner/repo\"]\n",
		"repeated key":        "[repos.\"owner/repo\"]\nallowlist = []\nallowlist = []\n",
		"missing comma":       "[repos.\"owner/repo\"]\nallowlist = [\"a\" \"b\"]\n",
	}
	for name, config := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.config = config
			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			want := "config.toml:2:"
			if name == "repeated key" {
				want = "config.toml:3:"
			}
			if !strings.Contains(w.stderr.String(), want) {
				t.Errorf("stderr should contain %q, got %q", want, w.stderr.String())
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}

func TestConfigWithRepositorySecrets(t *testing.T) {
	w := newWorld(t)
	w.secrets = coreSecrets + "STRIPE_KEY=" + stripeKey + "\n"
	w.config = stripeConfig

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s\nstdout:\n%s", code, w.stderr.String(), w.stdout.String())
	}
}

func TestUnreadableRepositorySecretsConfigIsRefusedNamingTheLine(t *testing.T) {
	const repo = "[repos.\"owner/repo\"]\n"
	cases := map[string]struct {
		config string
		line   string
		want   string
	}{
		"no domains":                          {repo + "[repos.\"owner/repo\".secrets]\nDB_PASSWORD = []\n", "config.toml:3:", "can't inject"},
		"a port":                              {repo + "[repos.\"owner/repo\".secrets]\nDB_PASSWORD = [\"db.example.com:5432\"]\n", "config.toml:3:", "can't inject"},
		"a URL":                               {repo + "[repos.\"owner/repo\".secrets]\nAPI_KEY = [\"https://api.example.com\"]\n", "config.toml:3:", "can't inject"},
		"every host under a top-level domain": {repo + "[repos.\"owner/repo\".secrets]\nAPI_KEY = [\"**.com\"]\n", "config.toml:3:", "can't inject"},
		"a single-label wildcard":             {repo + "[repos.\"owner/repo\".secrets]\nAPI_KEY = [\"*.com\"]\n", "config.toml:3:", "can't inject"},
		"a variable the sandbox relies on":    {repo + "[repos.\"owner/repo\".secrets]\nPATH = [\"a.example.com\"]\n", "config.toml:3:", "PATH"},
		"every host":                          {repo + "[repos.\"owner/repo\".secrets]\nAPI_KEY = [\"**\"]\n", "config.toml:3:", "can't inject"},
		"reserved name":                       {repo + "[repos.\"owner/repo\".secrets]\nGITHUB_TOKEN = [\"github.com\"]\n", "config.toml:3:", "GITHUB_TOKEN"},
		"not an env name":                     {repo + "[repos.\"owner/repo\".secrets]\nmy-key = [\"a.example.com\"]\n", "config.toml:3:", "my-key"},
		"repeated name":                       {repo + "[repos.\"owner/repo\".secrets]\nA = [\"a.com\"]\nA = [\"a.com\"]\n", "config.toml:4:", "twice"},
		"repeated table":                      {repo + "[repos.\"owner/repo\".secrets]\n[repos.\"owner/repo\".secrets]\n", "config.toml:3:", "twice"},
		"before its repository":               {"[repos.\"owner/repo\".secrets]\n", "config.toml:1:", "after"},
		"other sub-table":                     {repo + "[repos.\"owner/repo\".colours]\n", "config.toml:2:", "]"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			w.config = tc.config
			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			for _, want := range []string{tc.line, tc.want} {
				if !strings.Contains(w.stderr.String(), want) {
					t.Errorf("stderr should contain %q, got %q", want, w.stderr.String())
				}
			}
			if len(w.commands) != 0 {
				t.Errorf("commands sent: %v", w.commands)
			}
		})
	}
}
