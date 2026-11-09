package lightsoff

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// addRepo opts a repository in by appending its table to config.toml, with any
// users named by --allow. It only edits config: it sends no commands and needs
// no secrets.
func addRepo(repo string, users []string, env Env) error {
	if err := validateUserLogins(users); err != nil {
		return err
	}
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
		merged, added, already := appendUsers(c.cfg.repos[repo].allowlist, users)
		if len(added) > 0 {
			edited, err := c.withAllowlist(repo, merged)
			if err != nil {
				return err
			}
			if err := c.writeChecked(edited); err != nil {
				return err
			}
			printAddedUsers(env, added)
		}
		printAlreadyUsers(env, already)
		warnIfAllowlistEmpty(env, merged)
		return nil
	}

	merged, added, already := appendUsers(nil, users)
	edited := c.withRepo(repo, merged)
	if err := c.writeChecked(edited); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "lights-off: %s is opted in.\n", repo)
	if len(merged) == 0 {
		warnIfAllowlistEmpty(env, nil)
		return nil
	}
	printAddedUsers(env, added)
	printAlreadyUsers(env, already)
	return nil
}

// addAllowlistUsers adds users to an opted-in repository's Allowlist. It only
// edits config: it sends no commands and needs no secrets.
func addAllowlistUsers(repo string, users []string, env Env) error {
	if err := validateUserLogins(users); err != nil {
		return err
	}
	c, err := readConfigText(env.ConfigDir)
	if err != nil {
		return err
	}
	if !c.cfg.optedIn(repo) {
		return errors.New(notOptedInMessage(repo))
	}
	merged, added, already := appendUsers(c.cfg.repos[repo].allowlist, users)
	if len(added) == 0 {
		printAlreadyUsers(env, already)
		return nil
	}
	edited, err := c.withAllowlist(repo, merged)
	if err != nil {
		return err
	}
	if err := c.writeChecked(edited); err != nil {
		return err
	}
	printAddedUsers(env, added)
	printAlreadyUsers(env, already)
	return nil
}

// appendUsers adds the names that aren't already on the Allowlist, comparing
// ignoring case and keeping each existing name exactly as it is.
func appendUsers(existing, users []string) (merged, added, already []string) {
	merged = slices.Clone(existing)
	for _, user := range users {
		if slices.ContainsFunc(merged, func(u string) bool { return strings.EqualFold(u, user) }) {
			already = append(already, user)
			continue
		}
		merged = append(merged, user)
		added = append(added, user)
	}
	return merged, added, already
}

func printAddedUsers(env Env, users []string) {
	if len(users) == 0 {
		return
	}
	fmt.Fprintf(env.Stdout, "lights-off: added %s to the Allowlist.\n", strings.Join(users, ", "))
}

func printAlreadyUsers(env Env, users []string) {
	for _, user := range users {
		fmt.Fprintf(env.Stdout, "lights-off: %s is already on the Allowlist.\n", user)
	}
}

// notOptedInMessage is the refusal for a repository that isn't a Target
// Repository, with the command that would make it one.
func notOptedInMessage(repo string) string {
	return fmt.Sprintf("%s is not opted in. To opt it in, run:\n\n    lights-off repo add %s --allow=<your-github-username>", repo, repo)
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
	if err := c.writeChecked(edited); err != nil {
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
