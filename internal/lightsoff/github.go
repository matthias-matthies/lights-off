package lightsoff

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// shell runs gh, git and sbx, turning a non-zero exit into an error.
type shell struct {
	exec func(Command) (Result, error)
}

func (s shell) run(name string, args ...string) (string, error) {
	return s.runCommand(Command{Name: name, Args: args})
}

func (s shell) runCommand(c Command) (string, error) {
	res, err := s.exec(c)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", c.Name, c.Args[0], err)
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("%s %s exited %d: %s", c.Name, c.Args[0], res.ExitCode, lastLine(res.Stderr))
	}
	return res.Stdout, nil
}

// lastLine keeps a failure to one status line. dsh's stderr carries its whole
// reasoning, and its failure message comes last.
func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	return lines[len(lines)-1]
}

func (s shell) ghJSON(into any, args ...string) error {
	out, err := s.run("gh", args...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(out), into); err != nil {
		return fmt.Errorf("gh %s: unreadable output: %w", args[0], err)
	}
	return nil
}

type ticket struct {
	Number int        `json:"-"`
	Title  string     `json:"title"`
	Body   string     `json:"body"`
	State  string     `json:"state"`
	Labels labelNames `json:"labels"`
	URL    string     `json:"url"`
}

// readTicket fetches only the main text, never the comments, because comments
// can come from anyone and agents are given only trusted text.
func (s shell) readTicket(repo string, number int) (ticket, error) {
	var t ticket
	err := s.ghJSON(&t, "issue", "view", strconv.Itoa(number), "--repo", repo, "--json", "title,body,state,labels")
	t.Number = number
	return t, err
}

// listOpenTickets returns every open ticket, with the text and labels that
// eligibility needs and the URL a graph links to. It is only called when a
// Dashboard page asks for the graph, so a command with no page open makes no
// calls for it.
func (s shell) listOpenTickets(repo string) ([]ticket, error) {
	var listed []struct {
		Number int        `json:"number"`
		Title  string     `json:"title"`
		Body   string     `json:"body"`
		Labels labelNames `json:"labels"`
		URL    string     `json:"url"`
	}
	err := s.ghJSON(&listed, "issue", "list", "--repo", repo, "--state", "open",
		"--json", "number,title,body,labels,url", "--limit", "1000")
	if err != nil {
		return nil, err
	}
	tickets := make([]ticket, 0, len(listed))
	for _, t := range listed {
		tickets = append(tickets, ticket{
			Number: t.Number,
			Title:  t.Title,
			Body:   t.Body,
			// gh issue list answers only with open issues, but eligibility
			// reads a ticket's state from its main text, so fill it in here.
			State:  "OPEN",
			Labels: t.Labels,
			URL:    t.URL,
		})
	}
	slices.SortFunc(tickets, func(a, b ticket) int { return cmp.Compare(a.Number, b.Number) })
	return tickets, nil
}

// ghPages runs gh api --paginate and decodes each page, which gh prints one
// after another as separate JSON arrays.
func ghPages[T any](s shell, path string) ([]T, error) {
	out, err := s.run("gh", "api", "--paginate", path)
	if err != nil {
		return nil, err
	}
	var all []T
	decoder := json.NewDecoder(strings.NewReader(out))
	for decoder.More() {
		var page []T
		if err := decoder.Decode(&page); err != nil {
			return nil, fmt.Errorf("gh api %s: unreadable output: %w", path, err)
		}
		all = append(all, page...)
	}
	return all, nil
}

// nativeBlockers reads GitHub's native "blocked by" links, and whether each
// blocker is open.
func (s shell) nativeBlockers(repo string, number int) (map[issueRef]bool, error) {
	type blocker struct {
		Number  int    `json:"number"`
		State   string `json:"state"`
		HTMLURL string `json:"html_url"`
	}
	blockers, err := ghPages[blocker](s, fmt.Sprintf("repos/%s/issues/%d/dependencies/blocked_by", repo, number))
	if err != nil {
		return nil, err
	}
	open := map[issueRef]bool{}
	for _, b := range blockers {
		ref := issueRef{repo: repo, number: b.Number}
		if m := issueLink.FindStringSubmatch(b.HTMLURL); m != nil {
			ref.repo = m[1]
		}
		open[ref] = isOpen(b.State)
	}
	return open, nil
}

// issueState reads an issue's or pull request's state through the REST API,
// which answers for both, in the same casing as the native links.
func (s shell) issueState(ref issueRef) (string, error) {
	var issue struct {
		State string `json:"state"`
	}
	err := s.ghJSON(&issue, "api", fmt.Sprintf("repos/%s/issues/%d", ref.repo, ref.number))
	return issue.State, err
}

// isOpen reads a REST API state, which is open or closed.
func isOpen(state string) bool { return strings.EqualFold(state, "open") }

// lastLabelledBy returns who last applied label to the issue, from its label
// history, or "" if the history has no record of it.
func (s shell) lastLabelledBy(repo string, number int, label string) (string, error) {
	type event struct {
		Event string `json:"event"`
		Actor *struct {
			Login string `json:"login"`
		} `json:"actor"`
		Label *struct {
			Name string `json:"name"`
		} `json:"label"`
	}
	events, err := ghPages[event](s, fmt.Sprintf("repos/%s/issues/%d/events", repo, number))
	if err != nil {
		return "", err
	}
	by := ""
	for _, e := range events { // oldest first
		if e.Event == "labeled" && e.Label != nil && strings.EqualFold(e.Label.Name, label) {
			by = ""
			if e.Actor != nil {
				by = e.Actor.Login
			}
		}
	}
	return by, nil
}

type repoInfo struct {
	DefaultBranchRef struct {
		Name string `json:"name"`
	} `json:"defaultBranchRef"`
	ViewerDefaultMergeMethod string `json:"viewerDefaultMergeMethod"`
}

func (s shell) readRepo(repo string) (repoInfo, error) {
	var r repoInfo
	err := s.ghJSON(&r, "repo", "view", repo, "--json", "defaultBranchRef,viewerDefaultMergeMethod")
	return r, err
}

// pullRequest is read in one call so that its checks describe its head commit.
type pullRequest struct {
	Number      int     `json:"-"`
	State       string  `json:"state"`
	Body        string  `json:"body"`
	BaseRefName string  `json:"baseRefName"`
	HeadRefOid  string  `json:"headRefOid"`
	Mergeable   string  `json:"mergeable"`
	Checks      []check `json:"statusCheckRollup"`
}

// check is one entry of the rollup: a CheckRun or a StatusContext.
type check struct {
	TypeName   string `json:"__typename"`
	Name       string `json:"name"`       // CheckRun
	Context    string `json:"context"`    // StatusContext
	Status     string `json:"status"`     // CheckRun
	Conclusion string `json:"conclusion"` // CheckRun
	State      string `json:"state"`      // StatusContext
}

// conflicted is only ever a known conflict. GitHub answers UNKNOWN while it works
// that out, and a conflict it hadn't seen yet still fails the merge.
func (pr pullRequest) conflicted() bool { return pr.Mergeable == "CONFLICTING" }

type ciState int

const (
	ciPending ciState = iota
	ciRed
	ciGreen
	ciAbsent // no CI at all, which counts as green (ADR-0001)
)

// ci summarises the checks. Anything not known to be good or still running is
// red. Zero checks is pending, not green, so a timing gap can't skip tests: only
// the no-CI rule, which also looks at the repository, can make it ciAbsent.
func (pr pullRequest) ci() ciState {
	if len(pr.Checks) == 0 {
		return ciPending
	}
	state := ciGreen
	for _, check := range pr.Checks {
		switch check.state() {
		case ciRed:
			return ciRed
		case ciPending:
			state = ciPending
		}
	}
	return state
}

// failedChecks names the checks that made CI red, for the Implementer to look at.
func (pr pullRequest) failedChecks() []string {
	var names []string
	for _, check := range pr.Checks {
		if check.state() == ciRed {
			names = append(names, cmp.Or(check.Name, check.Context, "unnamed check"))
		}
	}
	return names
}

func (c check) state() ciState {
	if c.TypeName == "StatusContext" {
		switch c.State {
		case "SUCCESS":
			return ciGreen
		case "PENDING", "EXPECTED":
			return ciPending
		}
		return ciRed
	}
	if c.Status != "COMPLETED" {
		return ciPending
	}
	switch c.Conclusion {
	case "SUCCESS", "NEUTRAL", "SKIPPED":
		return ciGreen
	}
	return ciRed
}

// ciConfigured is the repository half of the no-CI rule: whether the default
// branch has a workflow file GitHub Actions would run or requires a status
// check. Anything it can't read is an error, so a gh problem can't pass for no CI.
func (s shell) ciConfigured(repo, branch string) (bool, error) {
	if found, err := s.hasWorkflows(repo, branch); err != nil || found {
		return found, err
	}
	return s.requiresChecks(repo, branch)
}

// ghAPIOrNone runs gh api, treating a failure whose message contains none as
// GitHub's way of saying there is nothing there. Any other failure is an error.
func (s shell) ghAPIOrNone(none string, args ...string) (out string, found bool, err error) {
	c := Command{Name: "gh", Args: append([]string{"api"}, args...)}
	res, err := s.exec(c)
	if err != nil {
		return "", false, fmt.Errorf("gh api: %w", err)
	}
	if res.ExitCode == 0 {
		return res.Stdout, true, nil
	}
	if strings.Contains(res.Stderr, none) {
		return "", false, nil
	}
	return "", false, fmt.Errorf("gh api exited %d: %s", res.ExitCode, lastLine(res.Stderr))
}

// hasWorkflows reads the default branch's .github/workflows. GitHub answers 404
// for a directory that doesn't exist.
func (s shell) hasWorkflows(repo, branch string) (bool, error) {
	out, found, err := s.ghAPIOrNone("HTTP 404", "repos/"+repo+"/contents/.github/workflows?ref="+url.QueryEscape(branch))
	if err != nil || !found {
		return false, err
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return false, fmt.Errorf("gh api: unreadable output: %w", err)
	}
	for _, entry := range entries {
		// GitHub Actions runs only these.
		if entry.Type == "file" && (strings.HasSuffix(entry.Name, ".yml") || strings.HasSuffix(entry.Name, ".yaml")) {
			return true, nil
		}
	}
	return false, nil
}

// requiresChecks looks at branch protection and at rulesets, because either
// can require a check.
func (s shell) requiresChecks(repo, branch string) (bool, error) {
	escaped := url.PathEscape(branch)
	var protected struct {
		Protection *struct {
			RequiredStatusChecks *struct {
				Contexts []string          `json:"contexts"`
				Checks   []json.RawMessage `json:"checks"`
			} `json:"required_status_checks"`
		} `json:"protection"`
	}
	if err := s.ghJSON(&protected, "api", "repos/"+repo+"/branches/"+escaped); err != nil {
		return false, err
	}
	// GitHub answers with empty lists for an unprotected branch, so a missing
	// field means the settings were withheld, not that there are none.
	if protected.Protection == nil || protected.Protection.RequiredStatusChecks == nil {
		return false, fmt.Errorf("gh api: branch %s answered without its required status checks", branch)
	}
	required := protected.Protection.RequiredStatusChecks
	if len(required.Contexts) > 0 || len(required.Checks) > 0 {
		return true, nil
	}

	// A private repository on GitHub Free can't have rulesets, and GitHub
	// refuses to list them with this message rather than answering none.
	types, _, err := s.ghAPIOrNone("Upgrade to GitHub Pro",
		"--paginate", "repos/"+repo+"/rules/branches/"+escaped, "--jq", ".[].type")
	if err != nil {
		return false, err
	}
	return slices.Contains(strings.Fields(types), "required_status_checks"), nil
}

func (s shell) readPullRequest(repo string, number int) (pullRequest, error) {
	var pr pullRequest
	err := s.ghJSON(&pr, "pr", "view", strconv.Itoa(number), "--repo", repo,
		"--json", "state,body,baseRefName,headRefOid,mergeable,statusCheckRollup")
	pr.Number = number
	return pr, err
}

// merge authenticates with GITHUB_TOKEN from secrets.env, not with whichever
// account gh is signed in to on this host.
func (s shell) merge(repo string, pr int, sha, method, token string) error {
	flagFor := map[string]string{"MERGE": "--merge", "SQUASH": "--squash", "REBASE": "--rebase"}
	flag, known := flagFor[method]
	if !known {
		return fmt.Errorf("unknown default merge method %q", method)
	}
	_, err := s.runCommand(Command{
		Name: "gh",
		Args: []string{"pr", "merge", strconv.Itoa(pr), "--repo", repo, flag, "--match-head-commit", sha},
		Env:  []string{"GH_TOKEN=" + token},
	})
	return err
}

// postVerdict is the audit trail, so an outcome doesn't happen without it. The
// Reviewer's token is read-only, so lights-off posts on its behalf.
func (s shell) postVerdict(repo string, pr int, v Verdict, sha string) error {
	_, err := s.runCommand(Command{
		Name:  "gh",
		Args:  []string{"pr", "comment", strconv.Itoa(pr), "--repo", repo, "--body-file", "-"},
		Stdin: fmt.Sprintf("**Lights-off Verdict** on %s: %s\n\n%s\n", sha, v.describe(), v.Reason),
	})
	return err
}

const (
	readyForAgent   = "ready-for-agent"
	agentInProgress = "agent-in-progress"
	readyForHuman   = "ready-for-human"
)

// labelNames matches gh's --json labels output.
type labelNames []struct {
	Name string `json:"name"`
}

func (l labelNames) find(name string) (string, bool) {
	for _, label := range l {
		if strings.EqualFold(label.Name, name) {
			return label.Name, true
		}
	}
	return "", false
}

// moveToReadyForHuman labels the ticket and, unless pr is 0, its pull request.
func (s shell) moveToReadyForHuman(repo string, number, pr int) error {
	if err := s.ensureLabel(repo, readyForHuman); err != nil {
		return err
	}
	if pr != 0 {
		if _, err := s.run("gh", "pr", "edit", strconv.Itoa(pr), "--repo", repo, "--add-label", readyForHuman); err != nil {
			return err
		}
	}
	return s.relabel(repo, number, readyForHuman, readyForAgent, agentInProgress)
}

// claim marks the ticket as taken, so that another Autonomous Run doesn't pick it up.
func (s shell) claim(repo string, number int) error {
	if err := s.ensureLabel(repo, agentInProgress); err != nil {
		return err
	}
	return s.relabel(repo, number, agentInProgress, readyForAgent)
}

// ensureLabel creates the label if the repository doesn't define it yet.
func (s shell) ensureLabel(repo, name string) error {
	var inRepo labelNames
	if err := s.ghJSON(&inRepo, "label", "list", "--repo", repo, "--json", "name", "--limit", "1000"); err != nil {
		return err
	}
	if _, exists := inRepo.find(name); exists {
		return nil
	}
	_, err := s.run("gh", "label", "create", name, "--repo", repo)
	return err
}

// relabel adds one label to the ticket and removes the others it carries. It
// names only labels known to exist, because gh fails the whole edit on one the
// repository doesn't define, even when removing it.
func (s shell) relabel(repo string, number int, add string, remove ...string) error {
	var onTicket struct {
		Labels labelNames `json:"labels"`
	}
	if err := s.ghJSON(&onTicket, "issue", "view", strconv.Itoa(number), "--repo", repo, "--json", "labels"); err != nil {
		return err
	}
	args := []string{"issue", "edit", strconv.Itoa(number), "--repo", repo, "--add-label", add}
	var carried []string
	for _, old := range remove {
		if name, found := onTicket.Labels.find(old); found {
			carried = append(carried, name)
		}
	}
	if len(carried) > 0 {
		args = append(args, "--remove-label", strings.Join(carried, ","))
	}
	_, err := s.run("gh", args...)
	return err
}

// checkout is the host-side Git repository that sbx create --clone copies. A
// clone sandbox starts at whatever the checkout has at HEAD.
type checkout struct {
	shell
	dir string
}

func checkoutDir(cacheDir, repo string) string {
	return filepath.Join(cacheDir, "repos", filepath.FromSlash(repo))
}

func openCheckout(s shell, cacheDir, repo string) (checkout, error) {
	c := checkout{shell: s, dir: checkoutDir(cacheDir, repo)}
	if _, err := os.Stat(filepath.Join(c.dir, ".git")); err == nil {
		return c, nil
	}
	if err := os.MkdirAll(filepath.Dir(c.dir), 0o755); err != nil {
		return c, err
	}
	// The URL form forces HTTPS whatever gh's git_protocol says, because the
	// sandbox's copy of origin can only authenticate through the HTTPS proxy.
	_, err := s.run("gh", "repo", "clone", "https://github.com/"+repo, c.dir)
	return c, err
}

// fetch authenticates through gh, because the clone's credentials don't persist.
func (c checkout) fetch(refspecs ...string) error {
	args := append([]string{"-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential",
		"-C", c.dir, "fetch", "origin"}, refspecs...)
	_, err := c.run("git", args...)
	return err
}

func (c checkout) moveTo(ref string) error {
	_, err := c.run("git", "-C", c.dir, "checkout", "--detach", ref)
	return err
}

func (c checkout) moveToBranch(branch string) error {
	if err := c.fetch("+refs/heads/" + branch + ":refs/remotes/origin/" + branch); err != nil {
		return err
	}
	return c.moveTo("refs/remotes/origin/" + branch)
}

// startBranch puts the checkout on a new local branch at the tip of base. A
// branch of that name left from an earlier session is reset, because the work
// on it lives in that session's sandbox or on GitHub, not here.
func (c checkout) startBranch(name, base string) error {
	if err := c.fetch("+refs/heads/" + base + ":refs/remotes/origin/" + base); err != nil {
		return err
	}
	_, err := c.run("git", "-C", c.dir, "checkout", "-B", name, "refs/remotes/origin/"+base)
	return err
}

// workflowsDir is where GitHub refuses a push that changes a file, because no
// token lights-off uses has the workflow scope (ADR-0003).
const workflowsDir = ".github/workflows/"

// changedWorkflowFiles returns the files under .github/workflows that any branch
// in the sandbox changed since base. It reads the sandbox's branches, not the
// pull request, because GitHub refuses the push and so the work may never reach
// a pull request. Fetching base first means a base commit the Implementer merged
// doesn't count as its change.
func (c checkout) changedWorkflowFiles(sandbox, base string) ([]string, error) {
	if err := c.fetch("+refs/heads/" + base + ":refs/remotes/origin/" + base); err != nil {
		return nil, err
	}
	refs := "refs/lights-off/sandboxes/" + sandbox
	if _, err := c.run("git", "-C", c.dir, "fetch", "sandbox-"+sandbox, "+refs/heads/*:"+refs+"/*"); err != nil {
		return nil, err
	}
	// --no-renames turns a rename out of the directory into a deletion and an
	// addition, so the old path is seen too. -z keeps a path whole whatever it
	// contains, and --format= leaves only paths in the output.
	out, err := c.run("git", "-C", c.dir, "log", "--format=", "--name-only", "--no-renames", "-z",
		"--glob="+refs+"/*", "--not", "refs/remotes/origin/"+base)
	if err != nil {
		return nil, err
	}
	var files []string
	seen := map[string]bool{}
	for _, name := range strings.Split(out, "\x00") {
		if strings.HasPrefix(name, workflowsDir) && !seen[name] {
			seen[name] = true
			files = append(files, name)
		}
	}
	slices.Sort(files)
	return files, nil
}

// moveToPullRequest puts the checkout on sha and returns the fixed point to
// review from: where the pull request's commits diverge from its base.
func (c checkout) moveToPullRequest(pr pullRequest, sha string) (string, error) {
	base := "refs/remotes/origin/" + pr.BaseRefName
	err := c.fetch("+refs/heads/"+pr.BaseRefName+":"+base,
		fmt.Sprintf("+refs/pull/%d/head:refs/lights-off/pull/%d", pr.Number, pr.Number))
	if err != nil {
		return "", err
	}
	out, err := c.run("git", "-C", c.dir, "merge-base", base, sha)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), c.moveTo(sha)
}

// listReadyTickets returns the open tickets labelled ready-for-agent, oldest
// first, so that selection is predictable. gh lists newest first and stops at
// --limit, so GitHub is asked for the oldest, or a large backlog would cut them
// off. A run starts few tickets, so the oldest thousand are plenty to pick from.
func (s shell) listReadyTickets(repo string) ([]int, error) {
	type listed struct {
		Number    int       `json:"number"`
		CreatedAt time.Time `json:"createdAt"`
	}
	var tickets []listed
	if err := s.ghJSON(&tickets, "issue", "list", "--repo", repo, "--state", "open", "--label", readyForAgent,
		"--search", "sort:created-asc", "--json", "number,createdAt", "--limit", "1000"); err != nil {
		return nil, err
	}
	slices.SortFunc(tickets, func(a, b listed) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.Number, b.Number))
	})
	numbers := make([]int, len(tickets))
	for i, t := range tickets {
		numbers[i] = t.Number
	}
	return numbers, nil
}

// closesTicket matches the closing keyword that merging must honour for number.
func closesTicket(number int) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`(?i)\bcloses\s+#%d\b`, number))
}

// openPullRequestClosing returns the open pull request whose body closes the
// ticket, or 0 if there is none. It reads bodies itself, because GitHub's search
// index can lag behind a pull request opened a moment ago.
func (s shell) openPullRequestClosing(repo string, number int) (int, error) {
	var open []struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	if err := s.ghJSON(&open, "pr", "list", "--repo", repo, "--state", "open", "--json", "number,body", "--limit", "1000"); err != nil {
		return 0, err
	}
	closes := closesTicket(number)
	for _, pr := range open {
		if closes.MatchString(pr.Body) {
			return pr.Number, nil
		}
	}
	return 0, nil
}
