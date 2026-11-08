package lightsoff

import "fmt"

// addRepo opts a repository in by appending its table to config.toml, with the
// users --allow gave it. It only edits config: it sends no commands, reads no
// secrets and touches no cache or state.
func addRepo(repo string, users []string, env Env) error {
	c, err := readConfigText(env.ConfigDir)
	if err != nil {
		return err
	}
	if c.cfg.optedIn(repo) {
		fmt.Fprintf(env.Stdout, "lights-off: %s is already opted in.\n", repo)
		if len(users) == 0 {
			warnIfAllowlistEmpty(env, c.cfg.repos[repo].allowlist)
			return nil
		}
		edited, added, already, err := c.addingUsers(repo, users)
		if err != nil {
			return err
		}
		// Re-running the command from the refusal must be safe, so an
		// Allowlist that already holds every user is left untouched.
		if len(added) > 0 {
			if err := c.writeChecked(edited); err != nil {
				return err
			}
		}
		reportAllowlist(env, repo, added, already)
		return nil
	}

	allowlist, _, _ := mergeUsers(nil, users)
	if err := c.writeChecked(c.withRepo(repo, allowlist)); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "lights-off: %s is opted in.\n", repo)
	reportAllowlist(env, repo, allowlist, nil)
	warnIfAllowlistEmpty(env, allowlist)
	return nil
}

// warnIfAllowlistEmpty explains what an empty Allowlist means for the next run.
func warnIfAllowlistEmpty(env Env, allowlist []string) {
	if len(allowlist) > 0 {
		return
	}
	fmt.Fprintln(env.Stderr, "lights-off: --n will pick up nothing until someone is on the Allowlist; --ticket still works with a confirmed override")
}
