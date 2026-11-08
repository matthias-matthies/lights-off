package lightsoff

import (
	"fmt"
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

// removeRepo opts a repository out by deleting its table and its .secrets table
// from config.toml. It only edits config: it sends no commands and never reads
// or writes secrets.env.
func removeRepo(repo string, env Env) error {
	c, err := readConfigText(env.ConfigDir)
	if err != nil {
		return err
	}
	if !c.cfg.optedIn(repo) {
		return fmt.Errorf("%s is not opted in, so there is nothing to remove", repo)
	}
	removedSecrets := c.cfg.repos[repo].secrets

	edited, removed := c.withoutRepo(repo)
	// Every edit is checked with the same parser the next run will use, so a
	// config lights-off wrote is one it can read back.
	if _, line, err := parseConfig(edited); err != nil {
		return fmt.Errorf("%s:%d: edited config.toml is invalid: %w", configPath(env.ConfigDir), line, err)
	}
	if err := c.write(edited); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "lights-off: %s is opted out.\n", repo)
	if removed != "" {
		fmt.Fprintf(env.Stdout, "Removed from %s:\n\n%s", configPath(env.ConfigDir), removed)
		if !strings.HasSuffix(removed, "\n") {
			fmt.Fprintln(env.Stdout)
		}
	}
	warnUndeclaredSecrets(env, secretsNoRemainingRepoDeclares(c.cfg, repo, removedSecrets))
	return nil
}

// secretsNoRemainingRepoDeclares names the secrets the opted-out repository
// declared that no other Target Repository does, in the order it declared them.
func secretsNoRemainingRepoDeclares(cfg config, removed string, secrets []repoSecret) []string {
	remaining := map[string]bool{}
	for name, repo := range cfg.repos {
		if name == removed {
			continue
		}
		for _, secret := range repo.secrets {
			remaining[secret.name] = true
		}
	}
	var names []string
	for _, secret := range secrets {
		if !remaining[secret.name] {
			names = append(names, secret.name)
		}
	}
	return names
}

// warnUndeclaredSecrets tells the person which values now have no declaration,
// so they can delete them from secrets.env themselves. Lights-off never edits
// that file, because only the person handles secret values.
func warnUndeclaredSecrets(env Env, names []string) {
	if len(names) == 0 {
		return
	}
	them := "them"
	if len(names) == 1 {
		them = "it"
	}
	fmt.Fprintf(env.Stderr, "lights-off: no remaining Target Repository declares %s; delete %s from secrets.env at %s, or lights-off will refuse to start.\n",
		strings.Join(names, ", "), them, secretsPath(env.ConfigDir))
}

// warnIfAllowlistEmpty explains what an empty Allowlist means for the next run.
func warnIfAllowlistEmpty(env Env, allowlist []string) {
	if len(allowlist) > 0 {
		return
	}
	fmt.Fprintln(env.Stderr, "lights-off: --n will pick up nothing until someone is on the Allowlist; --ticket still works with a confirmed override")
}
