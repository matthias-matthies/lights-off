package lightsoff

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// config is config.toml. It is read by a small parser for only the TOML subset
// lights-off uses, so that the binary has no dependencies. Anything outside that
// subset is an error rather than being skipped, so a config it can't fully read
// never decides whether a repository is opted in:
//
//	# comment
//	[repos."owner/repo"]
//	allowlist = ["user", "other-user"]  # one line, plain strings
//	implementer_memory = "4g"           # optional; megabytes (m) or gigabytes (g)
//
//	[repos."owner/repo".secrets]        # values live in secrets.env
//	STRIPE_KEY = ["api.stripe.com"]     # the domains the proxy injects it into
type config struct {
	repos map[string]repoConfig
}

type repoConfig struct {
	allowlist         []string
	secrets           []repoSecret
	implementerMemory string // empty for the starting limit
}

// implementerLimit is the Implementer's memory limit, as sbx --memory takes it.
func (r repoConfig) implementerLimit() string {
	return cmp.Or(r.implementerMemory, implementerMemory)
}

// repoSecret is a Target-Repository-specific secret. Having a domain to inject
// into is what makes it deliverable, so parsing refuses one without.
type repoSecret struct {
	name    string
	domains []string
}

func configPath(dir string) string { return filepath.Join(dir, "config.toml") }

// loadConfig treats a missing file as an empty config, so a first run gets the
// same refusal and instructions as any other repository not opted in.
func loadConfig(dir string) (config, error) {
	data, err := os.ReadFile(configPath(dir))
	if errors.Is(err, fs.ErrNotExist) {
		return config{}, nil
	}
	if err != nil {
		return config{}, err
	}
	cfg, line, err := parseConfig(string(data))
	if err != nil {
		return config{}, fmt.Errorf("%s:%d: %w", configPath(dir), line, err)
	}
	return cfg, nil
}

func (c config) optedIn(repo string) bool {
	_, ok := c.repos[repo]
	return ok
}

// declaresSecret looks across every repository, because secrets.env is shared by
// all of them.
func (c config) declaresSecret(variable string) bool {
	for _, repo := range c.repos {
		if slices.ContainsFunc(repo.secrets, func(s repoSecret) bool { return s.name == variable }) {
			return true
		}
	}
	return false
}

func configLinesFor(repo string) string {
	return fmt.Sprintf("[repos.%q]\nallowlist = [\"<your-github-username>\"]\n", repo)
}

// parseConfig returns the config, or an error and its 1-based line number.
func parseConfig(text string) (config, int, error) {
	cfg := config{repos: map[string]repoConfig{}}
	current := ""
	inSecrets := false
	seen := map[string]bool{} // settings already read, as repository and key
	seenSecrets := map[string]bool{}
	for i, line := range strings.Split(text, "\n") {
		p := &lineParser{rest: strings.TrimSpace(line)}
		var err error
		switch {
		case p.atEnd():
		case p.accept("["):
			current, inSecrets, err = p.table()
			switch _, known := cfg.repos[current]; {
			case err != nil:
			case inSecrets && !known:
				err = fmt.Errorf(`[repos.%q.secrets] must come after [repos.%q]`, current, current)
			case inSecrets && seenSecrets[current]:
				err = fmt.Errorf("secrets of %q appear twice", current)
			case inSecrets:
				seenSecrets[current] = true
			case known:
				err = fmt.Errorf("repository %q appears twice", current)
			default:
				cfg.repos[current] = repoConfig{}
			}
		case inSecrets:
			var secret repoSecret
			if secret, err = p.secret(); err == nil {
				repo := cfg.repos[current]
				if slices.ContainsFunc(repo.secrets, func(s repoSecret) bool { return s.name == secret.name }) {
					err = fmt.Errorf("%s appears twice", secret.name)
				}
				repo.secrets = append(repo.secrets, secret)
				cfg.repos[current] = repo
			}
		case current == "":
			err = errors.New(`settings must come after a [repos."owner/repo"] line`)
		default:
			err = p.setting(cfg, current, seen)
		}
		if err != nil {
			return config{}, i + 1, err
		}
	}
	return cfg, 0, nil
}

// lineParser consumes one line of config.toml.
type lineParser struct {
	rest string
}

func (p *lineParser) accept(token string) bool {
	p.rest = strings.TrimLeft(p.rest, " \t")
	if !strings.HasPrefix(p.rest, token) {
		return false
	}
	p.rest = p.rest[len(token):]
	return true
}

// atEnd reports whether only whitespace or a comment remains.
func (p *lineParser) atEnd() bool {
	p.rest = strings.TrimLeft(p.rest, " \t")
	return p.rest == "" || p.rest[0] == '#'
}

// quoted reads a plain string. Escapes are refused rather than interpreted.
func (p *lineParser) quoted() (string, error) {
	if !p.accept(`"`) {
		return "", errors.New("expected a quoted string")
	}
	end := strings.IndexByte(p.rest, '"')
	if end < 0 {
		return "", errors.New("unclosed string")
	}
	value := p.rest[:end]
	if strings.Contains(value, `\`) {
		return "", errors.New("escapes in strings are not supported")
	}
	p.rest = p.rest[end+1:]
	return value, nil
}

// table reads the remainder of a table header, after its opening bracket.
func (p *lineParser) table() (repo string, secrets bool, err error) {
	if !p.accept("repos.") {
		return "", false, errors.New(`expected a [repos."owner/repo"] line`)
	}
	if repo, err = p.quoted(); err != nil {
		return "", false, err
	}
	secrets = p.accept(".secrets")
	if !p.accept("]") {
		return "", false, errors.New(`expected "]"`)
	}
	return repo, secrets, p.expectEnd()
}

// setting reads a whole line of a repository's settings into cfg.
func (p *lineParser) setting(cfg config, repo string, seen map[string]bool) error {
	key, rest, _ := strings.Cut(p.rest, "=")
	key = strings.TrimSpace(key)
	if key != "allowlist" && key != "implementer_memory" {
		return errors.New("unknown setting: only allowlist and implementer_memory are supported")
	}
	if !strings.Contains(p.rest, "=") {
		return fmt.Errorf(`expected "=" after %s`, key)
	}
	if seen[repo+" "+key] {
		return fmt.Errorf("%s appears twice", key)
	}
	seen[repo+" "+key] = true
	p.rest = rest
	r := cfg.repos[repo]
	switch key {
	case "allowlist":
		allowlist, err := p.stringArray(key)
		if err != nil {
			return err
		}
		r.allowlist = allowlist
	default:
		memory, err := p.quoted()
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if _, err := memoryBytes(memory); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if err := p.expectEnd(); err != nil {
			return err
		}
		r.implementerMemory = memory
	}
	cfg.repos[repo] = r
	return nil
}

var memorySize = regexp.MustCompile(`^([1-9][0-9]{0,6})([mg])$`)

// memoryBytes reads a memory limit as sbx --memory takes it, such as 4g or
// 3072m. Only the forms both understand the same way are accepted.
func memoryBytes(limit string) (uint64, error) {
	m := memorySize.FindStringSubmatch(limit)
	if m == nil {
		return 0, fmt.Errorf("%q is not a memory size such as \"4g\" or \"3072m\"", limit)
	}
	n, _ := strconv.ParseUint(m[1], 10, 64)
	if m[2] == "g" {
		return n << 30, nil
	}
	return n << 20, nil
}

// injectableHost is what the proxy can match on: a host, or a wildcard over one
// domain's subdomains. It injects into HTTP headers, so a scheme or a port means
// the secret is for something else, such as a database connection. A wildcard
// needs a real domain, because over a whole top-level domain the agent could send
// the placeholder to a server of its own and read back the real value.
var injectableHost = regexp.MustCompile(`^([A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*|\*{1,2}(\.[A-Za-z0-9-]+){2,})$`)

// sandboxVariables can't name a secret, because lights-off exports the secret's
// placeholder under that name in the sandbox's shells.
var sandboxVariables = []string{"BASH_ENV", "ENV", "GH_TOKEN", "HOME", "LD_LIBRARY_PATH", "LD_PRELOAD", "PATH", "SHELL", "USER"}

// secret reads a whole `NAME = ["domain", ...]` line, refusing a secret the sbx
// proxy couldn't inject.
func (p *lineParser) secret() (repoSecret, error) {
	name, rest, found := strings.Cut(p.rest, "=")
	name = strings.TrimSpace(name)
	switch {
	case !found:
		return repoSecret{}, errors.New(`expected NAME = ["domain"]`)
	case !secretName.MatchString(name):
		return repoSecret{}, fmt.Errorf("%q is not an environment variable name", name)
	case isCoreSecret(name):
		return repoSecret{}, fmt.Errorf("%s is reserved: lights-off delivers it itself", name)
	case slices.Contains(sandboxVariables, name):
		return repoSecret{}, fmt.Errorf("%s is reserved: the sandbox's shells rely on it", name)
	}
	p.rest = rest
	domains, err := p.stringArray(name)
	if err != nil {
		return repoSecret{}, err
	}
	if len(domains) == 0 {
		return repoSecret{}, fmt.Errorf("%s has no domains, so the sbx proxy can't inject it: run what needs it in CI", name)
	}
	for _, domain := range domains {
		if !injectableHost.MatchString(domain) {
			return repoSecret{}, fmt.Errorf("%s: the sbx proxy can't inject into %q, which is not a host name: run what needs it in CI", name, domain)
		}
	}
	return repoSecret{name: name, domains: domains}, nil
}

// stringArray reads a one-line array of plain strings and the end of the line.
func (p *lineParser) stringArray(name string) ([]string, error) {
	if !p.accept("[") {
		return nil, fmt.Errorf("%s must be an array of strings", name)
	}
	var items []string
	for !p.accept("]") {
		item, err := p.quoted()
		if err != nil {
			return nil, fmt.Errorf("%s must be an array on one line: %w", name, err)
		}
		items = append(items, item)
		if !p.accept(",") && !strings.HasPrefix(strings.TrimLeft(p.rest, " \t"), "]") {
			return nil, fmt.Errorf(`expected "," or "]" in %s`, name)
		}
	}
	return items, p.expectEnd()
}

func (p *lineParser) expectEnd() error {
	if !p.atEnd() {
		return fmt.Errorf("unexpected text: %q", p.rest)
	}
	return nil
}
