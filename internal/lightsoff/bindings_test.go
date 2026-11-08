package lightsoff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	deepseekBinding = "  deepseek:\n    apiKey:\n      domains: [api.deepseek.com]\n"
	allGitHub       = "[api.github.com, github.com, raw.githubusercontent.com, gist.github.com, copilot.github.com, api.githubcopilot.com]"
	githubBinding   = "  github:\n    apiKey:\n      domains: " + allGitHub + "\n"
)

func TestCredentialBindingsAreMergedWithoutTouchingUnrelatedEntries(t *testing.T) {
	cases := map[string]struct{ before, after string }{
		"empty file": {
			"",
			"bindings:\n" + deepseekBinding + githubBinding,
		},
		"unrelated service, comments and other keys": {
			"# mine\nversion: 1\nbindings:\n  # work\n  anthropic:\n    apiKey:\n      domains: [api.anthropic.com] # ok\n\nother:\n  key: value\n",
			"# mine\nversion: 1\nbindings:\n  # work\n  anthropic:\n    apiKey:\n      domains: [api.anthropic.com] # ok\n" +
				deepseekBinding + githubBinding + "\nother:\n  key: value\n",
		},
		"no bindings key yet": {
			"version: 1\n",
			"version: 1\nbindings:\n" + deepseekBinding + githubBinding,
		},
		"empty bindings": {
			"bindings: {}\n",
			"bindings:\n" + deepseekBinding + githubBinding,
		},
		"already complete": {
			"bindings:\n" + deepseekBinding + githubBinding,
			"bindings:\n" + deepseekBinding + githubBinding,
		},
		"a service with too few domains keeps its own": {
			"bindings:\n" + deepseekBinding + "  github:\n    apiKey:\n      domains: [github.com, example.com]\n",
			"bindings:\n" + deepseekBinding + "  github:\n    apiKey:\n      domains: [github.com, example.com, api.github.com, raw.githubusercontent.com, gist.github.com, copilot.github.com, api.githubcopilot.com]\n",
		},
		"domains written as a block list": {
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n      - example.com\n" + githubBinding,
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n      - example.com\n      - api.deepseek.com\n" + githubBinding,
		},
		"quoted keys and domains": {
			"bindings:\n  \"deepseek\":\n    apiKey:\n      domains: [\"api.deepseek.com\"]\n" + githubBinding,
			"bindings:\n  \"deepseek\":\n    apiKey:\n      domains: [\"api.deepseek.com\"]\n" + githubBinding,
		},
		"comments among block list items": {
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n      # why\n      - example.com # ok\n" + githubBinding,
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n      # why\n      - example.com # ok\n      - api.deepseek.com\n" + githubBinding,
		},
		"a block list item already there with a comment": {
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n        - api.deepseek.com # ok\n" + githubBinding,
			"bindings:\n  deepseek:\n    apiKey:\n      domains:\n        - api.deepseek.com # ok\n" + githubBinding,
		},
		"a space before the colon": {
			"bindings:\n  deepseek :\n    apiKey:\n      domains: [api.deepseek.com]\n" + githubBinding,
			"bindings:\n  deepseek :\n    apiKey:\n      domains: [api.deepseek.com]\n" + githubBinding,
		},
		"comments on edited lines": {
			"bindings: {} # keep me\n",
			"bindings: # keep me\n" + deepseekBinding + githubBinding,
		},
		"a comment on an extended flow list": {
			"bindings:\n  deepseek:\n    apiKey:\n      domains: [example.com] # why\n" + githubBinding,
			"bindings:\n  deepseek:\n    apiKey:\n      domains: [example.com, api.deepseek.com] # why\n" + githubBinding,
		},
		"a service approved only for OAuth keeps it": {
			"bindings:\n  deepseek:\n    oauth:\n      domains: [auth.example.com]\n" + githubBinding,
			"bindings:\n  deepseek:\n    apiKey:\n      domains: [api.deepseek.com]\n    oauth:\n      domains: [auth.example.com]\n" + githubBinding,
		},
		"wider indentation": {
			"bindings:\n    anthropic:\n        apiKey:\n            domains: [api.anthropic.com]\n",
			"bindings:\n    anthropic:\n        apiKey:\n            domains: [api.anthropic.com]\n" +
				"    deepseek:\n        apiKey:\n            domains: [api.deepseek.com]\n" +
				"    github:\n        apiKey:\n            domains: " + allGitHub + "\n",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			path := filepath.Join(w.sbxConfigDir, "credentials.yaml")
			if err := os.WriteFile(path, []byte(tc.before), 0o640); err != nil {
				t.Fatal(err)
			}

			if code := w.run("owner/repo", "--ticket=42"); code != 0 {
				t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
			}

			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.after {
				t.Errorf("credentials.yaml:\n%s\nwant:\n%s", got, tc.after)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o640 {
				t.Errorf("mode = %o, want it kept", info.Mode().Perm())
			}
		})
	}
}

func TestCredentialBindingsExistBeforeAnySandboxIsCreated(t *testing.T) {
	w := newWorld(t)
	w.sbxConfigDir = filepath.Join(w.sbxConfigDir, "not", "yet")

	if code := w.run("owner/repo", "--ticket=42"); code != 0 {
		t.Fatalf("exit code = %d\nstderr:\n%s", code, w.stderr.String())
	}
	want := "bindings:\n" + deepseekBinding + githubBinding
	if w.bindingsAtCreate != want {
		t.Errorf("credentials.yaml when the first sandbox was created:\n%s\nwant:\n%s", w.bindingsAtCreate, want)
	}
}

func TestCredentialBindingsItCannotMergeAreRefusedAndLeftAlone(t *testing.T) {
	cases := map[string]string{
		"flow-style service":           "bindings:\n  deepseek: {apiKey: {domains: [api.deepseek.com]}}\n",
		"flow-style bindings":          "bindings: {deepseek: {}}\n",
		"bindings is a list":           "bindings:\n  - deepseek\n",
		"list at the key's own indent": "bindings:\n- deepseek\n",
		"flow-style root":              "{}\n",
		"document end marker":          "bindings:\n  other:\n    apiKey:\n      domains: [a.com]\n...\n",
		"document start marker":        "---\nbindings:\n",
		"domains on the next line":     "bindings:\n  deepseek:\n    apiKey:\n      domains:\n        [api.deepseek.com]\n",
		"tabs":                         "bindings:\n\tdeepseek:\n",
		"domains not a list":           "bindings:\n  deepseek:\n    apiKey:\n      domains: api.deepseek.com\n",
	}
	for name, before := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			path := filepath.Join(w.sbxConfigDir, "credentials.yaml")
			if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
				t.Fatal(err)
			}

			if code := w.run("owner/repo", "--ticket=42"); code == 0 {
				t.Errorf("exit code = 0, want non-zero")
			}
			if got, _ := os.ReadFile(path); string(got) != before {
				t.Errorf("file changed:\n%s", got)
			}
			if !strings.Contains(w.stderr.String(), "credentials.yaml") || !strings.Contains(w.stderr.String(), "api.deepseek.com") {
				t.Errorf("stderr should name the file and what to add, got %q", w.stderr.String())
			}
			if got := w.calls("sbx", "create"); len(got) != 0 {
				t.Errorf("sandbox created: %v", got)
			}
		})
	}
}
