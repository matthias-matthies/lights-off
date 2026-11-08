package lightsoff

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// userLogin is GitHub's user-login format: letters, digits and single hyphens,
// with no leading or trailing hyphen. The length limit is checked separately.
var userLogin = regexp.MustCompile(`^[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

// validateLogins refuses names the CLI must not write to an Allowlist. The
// parser stays lenient, so a hand-edited file can still hold any plain string.
func validateLogins(users []string) error {
	for _, user := range users {
		if len(user) < 1 || len(user) > 39 || !userLogin.MatchString(user) {
			return fmt.Errorf("%q is not a GitHub user login: letters, digits and single hyphens, 1-39 characters, with no leading or trailing hyphen", user)
		}
	}
	return nil
}

// parseAllowFlag reads repo add's comma-separated --allow users.
func parseAllowFlag(value string, set bool) ([]string, error) {
	if !set {
		return nil, nil
	}
	if value == "" {
		return nil, errors.New("--allow needs at least one user")
	}
	users := strings.Split(value, ",")
	if err := validateLogins(users); err != nil {
		return nil, err
	}
	return users, nil
}

// optInHint is the refusal for a repository that isn't a Target Repository. The
// command it prints is safe to re-run on one that is already opted in.
func optInHint(repo string) string {
	return fmt.Sprintf("%s is not opted in. To opt it in, run:\n\n    lights-off repo add %s --allow=<your-github-username>", repo, repo)
}

// mergeUsers adds the users the Allowlist doesn't already hold, comparing
// ignoring case. Names are kept exactly as typed, and each one is reported once.
func mergeUsers(existing, users []string) (merged, added, already []string) {
	merged = slices.Clone(existing)
	for _, user := range users {
		if slices.ContainsFunc(merged, func(onList string) bool { return strings.EqualFold(onList, user) }) {
			already = append(already, user)
			continue
		}
		merged = append(merged, user)
		added = append(added, user)
	}
	return merged, added, already
}

// addAllowlist adds users to an opted-in repository's Allowlist. It only edits
// config: it sends no commands, reads no secrets and touches no cache or state.
func addAllowlist(repo string, users []string, env Env) error {
	c, err := readConfigText(env.ConfigDir)
	if err != nil {
		return err
	}
	if !c.cfg.optedIn(repo) {
		return errors.New(optInHint(repo))
	}
	edited, added, already, err := c.addingUsers(repo, users)
	if err != nil {
		return err
	}
	if len(added) > 0 {
		if err := c.writeChecked(edited); err != nil {
			return err
		}
	}
	reportAllowlist(env, repo, added, already)
	return nil
}

// reportAllowlist says what changed, and which names the Allowlist already held.
func reportAllowlist(env Env, repo string, added, already []string) {
	if len(added) > 0 {
		fmt.Fprintf(env.Stdout, "lights-off: added %s to the Allowlist of %s.\n", strings.Join(added, ", "), repo)
	}
	for _, user := range already {
		fmt.Fprintf(env.Stdout, "lights-off: %s is already on the Allowlist of %s.\n", user, repo)
	}
}
