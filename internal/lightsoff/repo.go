package lightsoff

import "fmt"

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
