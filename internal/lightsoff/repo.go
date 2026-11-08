package lightsoff

import (
	"fmt"
	"maps"
	"slices"
	"strings"
)

// addRepo opts a repository in by appending its table to config.toml. It only
// edits config: it sends no commands and needs no secrets.
func addRepo(repo string, env Env) error {
	c, err := readConfigText(env.ConfigDir)
	if err != nil {
		return err
	}
	if c.cfg.optedIn(repo) {
		fmt.Fprintf(env.Stdout, "lights-off: %s is already opted in.\n", repo)
		warnIfAllowlistEmpty(env, c.cfg.repos[repo].allowlist)
		return nil
	}

	edited := c.withRepo(repo)
	// Every edit is checked with the same parser the next run will use, so a
	// config lights-off wrote is one it can read back.
	if _, line, err := parseConfig(edited); err != nil {
		return fmt.Errorf("%s:%d: edited config.toml is invalid: %w", configPath(env.ConfigDir), line, err)
	}
	if err := c.write(edited); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "lights-off: %s is opted in.\n", repo)
	warnIfAllowlistEmpty(env, nil)
	return nil
}

// warnIfAllowlistEmpty explains what an empty Allowlist means for the next run.
func warnIfAllowlistEmpty(env Env, allowlist []string) {
	if len(allowlist) > 0 {
		return
	}
	fmt.Fprintln(env.Stderr, "lights-off: --n will pick up nothing until someone is on the Allowlist; --ticket still works with a confirmed override")
}

// listRepos shows every Target Repository and its Allowlist. It reads config
// only: it writes nothing, sends no commands and never shows secrets or memory
// settings.
func listRepos(env Env) error {
	cfg, err := loadConfig(env.ConfigDir)
	if err != nil {
		return err
	}
	if len(cfg.repos) == 0 {
		fmt.Fprintln(env.Stdout, "lights-off: no Target Repositories are opted in. To opt one in, run:")
		fmt.Fprintln(env.Stdout)
		fmt.Fprintln(env.Stdout, "    lights-off repo add <owner>/<repo>")
		return nil
	}
	for _, repo := range slices.Sorted(maps.Keys(cfg.repos)) {
		allowlist := cfg.repos[repo].allowlist
		if len(allowlist) == 0 {
			fmt.Fprintf(env.Stdout, "%s: (empty allowlist)\n", repo)
			continue
		}
		fmt.Fprintf(env.Stdout, "%s: %s\n", repo, strings.Join(allowlist, ", "))
	}
	return nil
}
