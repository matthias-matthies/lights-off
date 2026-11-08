package lightsoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

func secretsPath(configDir string) string { return filepath.Join(configDir, "secrets.env") }

// grant delivers one secrets.env variable to one sbx service.
type grant struct {
	service  string
	variable string
}

// A sandbox-scoped value takes precedence, so both roles use the kit's github
// service and differ only in the token stored for their sandbox.
var (
	implementerGrants = []grant{{"deepseek", "DEEPSEEK_API_KEY"}, {"github", "GITHUB_TOKEN"}}
	reviewerGrants    = []grant{{"deepseek", "DEEPSEEK_API_KEY"}, {"github", "GITHUB_REVIEWER_TOKEN"}}
)

func isCoreSecret(variable string) bool {
	return slices.ContainsFunc(slices.Concat(implementerGrants, reviewerGrants),
		func(g grant) bool { return g.variable == variable })
}

// secrets holds the values from secrets.env. No value may reach an argument, an
// error or a status line, so nothing here formats one.
type secrets map[string]string

// loadSecrets reads secrets.env, refusing a file anyone else could read, any
// secret the proxy couldn't inject, and a missing value for any of required.
func loadSecrets(configDir string, cfg config, repo string, required []grant) (secrets, error) {
	path := secretsPath(configDir)
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s does not exist: create it with mode 0600 and the keys lights-off needs", path)
	}
	if err != nil {
		return nil, err
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		return nil, fmt.Errorf("%s has mode %04o: it must be 0600", path, perm)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	values, line, err := parseSecrets(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s:%d: %w", path, line, err)
	}
	for _, g := range required {
		if values[g.variable] == "" {
			return nil, fmt.Errorf("%s has no value for %s", path, g.variable)
		}
	}
	for _, secret := range cfg.repos[repo].secrets {
		if values[secret.name] == "" {
			return nil, fmt.Errorf("%s has no value for %s, which config.toml declares for %s", path, secret.name, repo)
		}
	}
	for variable := range values {
		if !isCoreSecret(variable) && !cfg.declaresSecret(variable) {
			return nil, fmt.Errorf("%s in %s has no domains in config.toml, so the sbx proxy can't inject it. "+
				"Lights-off refuses to deliver a secret any other way: list its domains, or run what needs it in CI", variable, path)
		}
	}
	return values, nil
}

var secretName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// parseSecrets returns the values, or an error and its 1-based line number. Its
// errors never quote the file, because a malformed line can be a real key.
func parseSecrets(text string) (secrets, int, error) {
	values := secrets{}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		name, value, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !secretName.MatchString(name) {
			return nil, i + 1, errors.New("expected NAME=value")
		}
		if _, repeated := values[name]; repeated {
			return nil, i + 1, fmt.Errorf("%s appears twice", name)
		}
		value = strings.TrimSpace(value)
		if n := len(value); n >= 2 && (value[0] == '"' || value[0] == '\'') && value[n-1] == value[0] {
			value = value[1 : n-1]
		} else if strings.ContainsAny(value, " \t") {
			// Most likely a comment, which would otherwise become part of the key.
			return nil, i + 1, fmt.Errorf("the value of %s has a space: put comments on their own line, or quote it", name)
		}
		values[name] = value
	}
	return values, 0, nil
}

// checkNotMounted refuses a secrets file an agent could read directly. It looks
// at the file both as named and with symlinks resolved, because either can be
// what puts it inside a mount.
func checkNotMounted(path string, mounts []string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	for _, file := range []string{path, real} {
		for _, mount := range mounts {
			if isInside(file, mount) {
				return fmt.Errorf("%s is in %s, which is mounted into a sandbox: move it, because an agent could read it", path, mount)
			}
		}
	}
	return nil
}

// isInside reports whether path is mount or lies under it. It compares files
// rather than path text, so that symlinks and case-insensitive file systems can't
// hide a match. A mount that doesn't exist can't contain anything.
func isInside(path, mount string) bool {
	target, err := os.Stat(mount)
	if err != nil {
		return false
	}
	for {
		info, err := os.Stat(path)
		if err == nil && os.SameFile(info, target) {
			return true
		}
		parent := filepath.Dir(path)
		if parent == path {
			return false
		}
		path = parent
	}
}

// sandboxMounts lists what existing sandboxes have mounted. sbx ls --json prints
// them under mounts and workspaces, and only mounts has the ones sbx adds itself.
// That schema isn't documented and mounts isn't always there, so every absolute
// path in the output counts: a renamed field can't quietly empty this check.
// Output that can't be read is an error rather than an empty list.
func (s shell) sandboxMounts() ([]string, error) {
	out, err := s.run("sbx", "ls", "--json")
	if err != nil {
		return nil, err
	}
	var listing any
	if err := json.Unmarshal([]byte(out), &listing); err != nil {
		return nil, fmt.Errorf("sbx ls: unreadable output: %w", err)
	}
	return absolutePaths(listing, nil), nil
}

// absolutePaths collects every host path that a string in value could name. A
// mount reads host:target:ro, but a colon can also be part of a host folder's
// name and the text doesn't show which is which. So each text before a colon
// counts, as does the whole string: a path that doesn't exist matches nothing,
// and a wrong match refuses a run rather than exposing a secret.
func absolutePaths(value any, found []string) []string {
	switch v := value.(type) {
	case string:
		if !filepath.IsAbs(v) {
			return found
		}
		for i, char := range v {
			if char == ':' {
				found = append(found, v[:i])
			}
		}
		found = append(found, v)
	case []any:
		for _, item := range v {
			found = absolutePaths(item, found)
		}
	case map[string]any:
		for _, item := range v {
			found = absolutePaths(item, found)
		}
	}
	return found
}

// checkValueStdin fails early on an sbx too old to take a custom secret's value
// from stdin, which would otherwise only show once a sandbox exists. Lights-off
// won't fall back to the flags that put a value in the arguments.
func (s shell) checkValueStdin(repoSecrets []repoSecret) error {
	if len(repoSecrets) == 0 {
		return nil
	}
	help, err := s.run("sbx", "secret", "set-custom", "--help")
	if err != nil {
		return err
	}
	if !strings.Contains(help, "--value-stdin") {
		return fmt.Errorf("this sbx has no secret set-custom --value-stdin, which delivering %s needs: update sbx", repoSecrets[0].name)
	}
	return nil
}
