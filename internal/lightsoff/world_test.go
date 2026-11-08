package lightsoff_test

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

const (
	headSHA   = "aaaa1111"
	mergeBase = "bbbb2222"

	greenChecks   = `[{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"SUCCESS"}]`
	pendingChecks = `[{"__typename":"CheckRun","name":"test","status":"IN_PROGRESS","conclusion":""}]`
	redChecks     = `[{"__typename":"CheckRun","name":"test","status":"COMPLETED","conclusion":"FAILURE"}]`

	finishedResult = `{"status":"finished","pr":7,"reason":"done"}`
	mergeVerdict   = `{"mergeable":true,"risk":"low","update":"two-way","reason":"fine"}`
	oneWayVerdict  = `{"mergeable":true,"risk":"low","update":"one-way","reason":"drops a table"}`

	deepseekKey   = "sk-deepseek-value"
	implementerGH = "ghp-implementer-value"
	reviewerGH    = "ghp-reviewer-value"
	stripeKey     = "sk-stripe-value"

	coreSecrets = "DEEPSEEK_API_KEY=" + deepseekKey + "\n" +
		"GITHUB_TOKEN=" + implementerGH + "\n" +
		"GITHUB_REVIEWER_TOKEN=" + reviewerGH + "\n"
)

// world is the fake outside world: scripted GitHub state and agent answers for
// gh, git and sbx, plus the clock, host memory and the terminal. Tests observe
// lights-off only through its output, exit code and the commands it sends here.
type world struct {
	mu sync.Mutex // lights-off may run tickets at once, so every call in here takes it

	t            *testing.T
	configDir    string
	cacheDir     string
	sbxConfigDir string
	stateDir     string
	config       string
	secrets      string      // secrets.env, written unless skipSecrets
	secretsPerm  os.FileMode // its permissions
	skipSecrets  bool
	listing      string // what sbx ls --json prints

	defaultBranch string

	ticketTitle  string
	ticketBody   string
	ticketState  string
	ticketLabels []string             // on the ticket
	labelEvents  []labelEvent         // the ticket's label history, oldest first
	eventsExit   int                  // reading the label history fails
	blockedBy    []string             // native blockers, as owner/repo#N
	blockedExit  int                  // reading the native blockers fails
	issueStates  map[string]string    // other issues' states, by owner/repo#N; others don't exist
	ticketOpened time.Time            // when #42 was opened
	others       map[int]*otherTicket // tickets besides #42, for runs over several
	closeDelay   time.Duration        // how long after a merge GitHub closes the ticket
	closesAt     map[int]time.Time    // tickets merged and not yet closed

	terminal       bool     // a terminal is attached to stdin
	stdoutTerminal *bool    // whether stdout is a terminal; nil means it follows terminal
	input          string   // what the person types
	repoLabels     []string // defined in the repository
	prLabels       []string // on the pull request
	prBody         string
	prState        string
	prBase         string
	checks         []string // one per poll; the last repeats

	// CI configured on the default branch.
	workflowFiles    []string // under .github/workflows; none answers 404, like GitHub
	protectedChecks  []string // required by branch protection
	rulesetChecks    []string // required by a ruleset
	workflowsExit    int      // listing the workflow files fails, other than with 404
	protectionHidden bool     // the branch answers without its protection settings
	rulesExit        string   // why reading the rulesets fails: "plan" or "permission"

	implementerOutput string
	implementerExit   int
	reviewerOutput    string
	reviewerExit      int
	implementerPasses []agentPass // one per pass; the outputs above answer once these run out
	implementerFiles  [][]string  // the files each Implementer pass leaves in its sandbox; the last repeats
	reviewerPasses    []agentPass // likewise, one per Reviewer
	heads             []string    // the pull request's head after each Implementer pass; the last repeats
	conflictedPasses  int         // the pull request conflicts until an Implementer pass after this many
	conflictAtMerge   bool        // the base moves during review: the first merge fails on a new conflict
	unknownAfterMerge int         // GitHub answers UNKNOWN this many times after that failed merge
	closedFromPass    int         // the pull request is closed from this Implementer pass on
	mergeExit         int
	commentExit       int
	labelEditExit     int
	prLabelExit       int
	stopExit          int
	rmExit            int  // sbx rm's exit code; by itself it means the secret cleanup failed
	rmLeavesSandbox   bool // sbx rm failed before removing the sandbox
	rmCleansDeepseek  bool // sbx rm's failed cleanup got as far as deleting deepseek
	secretSetExit     int
	noValueStdin      bool // an sbx too old for set-custom --value-stdin
	secretRmExit      int
	sessionExit       int // how the person's sbx run session ends
	hostMemory        uint64

	// hold keeps each Implementer pass running, in real time, until this many
	// agents run at once or holdFor passes, so that tickets lights-off runs at
	// once are seen to overlap.
	hold               int
	holdFor            time.Duration
	running            int // agents running now
	mostAtOnce         int // the most agents that ran at once
	holdUntilMerged    int // #42's first Implementer pass waits until this ticket's pull request merged
	conflictWhenMerged int // merging this ticket's pull request leaves #42's in conflict

	// Ctrl+C. interruptWhen presses it as a command reaches the outside world,
	// and interruptAtOnce once this many agents run at once. An agent running
	// then is killed, as the real Stop does.
	interrupt       chan struct{}
	interrupted     bool
	catching        bool // lights-off asked to catch Ctrl+C
	interruptWhen   func(lightsoff.Command) bool
	interruptAtOnce int
	// The Implementer pass Ctrl+C killed had already opened pull request #7.
	prOpenedByKilledPass bool

	now              time.Time
	sandboxes        []string
	stopped          map[string]bool // sandboxes sbx stop stopped and no sbx exec restarted
	implementerRuns  int
	reviewerRuns     int
	conflictedAtHead bool
	unknownReads     int
	overlaps         []string                     // what else of the same ticket was running whenever an agent ran
	gone             map[string]bool              // sandboxes sbx rm removed
	scoped           map[string]map[string]string // sandbox -> secret -> value
	commands         []lightsoff.Command
	reportedEvents   []lightsoff.Event // every structured ticket event reported

	bindingsAtCreate string            // credentials.yaml as the first sandbox saw it
	checkoutHead     string            // what the host checkout has at HEAD, from git checkout
	clonedFrom       map[string]string // each sandbox's name -> the checkout HEAD sbx create cloned

	openURLs []string // the URLs lights-off opened in the browser
	listen   func(network, address string) (net.Listener, error)
	stdout   bytes.Buffer
	stderr   bytes.Buffer
	version  string
}

func newWorld(t *testing.T) *world {
	return &world{
		t:                 t,
		configDir:         t.TempDir(),
		cacheDir:          t.TempDir(),
		sbxConfigDir:      t.TempDir(),
		stateDir:          t.TempDir(),
		secrets:           coreSecrets,
		secretsPerm:       0o600,
		listing:           "[]",
		interrupt:         make(chan struct{}),
		gone:              map[string]bool{},
		stopped:           map[string]bool{},
		clonedFrom:        map[string]string{},
		heads:             []string{headSHA},
		scoped:            map[string]map[string]string{},
		config:            "[repos.\"owner/repo\"]\nallowlist = [\"owner\"]\n",
		defaultBranch:     "main",
		ticketTitle:       "Add sum",
		ticketBody:        "Make sum add numbers.",
		ticketState:       "OPEN",
		ticketLabels:      []string{"ready-for-agent"},
		labelEvents:       []labelEvent{{"labeled", "ready-for-agent", "owner"}},
		issueStates:       map[string]string{},
		ticketOpened:      time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC),
		others:            map[int]*otherTicket{},
		closesAt:          map[int]time.Time{},
		repoLabels:        []string{"ready-for-agent", "ready-for-human", "agent-in-progress"},
		prBody:            "## Summary\n\nCloses #42\n",
		prState:           "OPEN",
		prBase:            "main",
		checks:            []string{greenChecks},
		workflowFiles:     []string{"ci.yml"},
		implementerOutput: finishedResult,
		reviewerOutput:    mergeVerdict,
		now:               time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC),
		hostMemory:        16 << 30,
	}
}

func (w *world) run(args ...string) int {
	w.t.Helper()
	return w.runWithExec(w.exec, args...)
}

func (w *world) runWithExec(exec func(lightsoff.Command) (lightsoff.Result, error), args ...string) int {
	w.t.Helper()
	if err := os.WriteFile(filepath.Join(w.configDir, "config.toml"), []byte(w.config), 0o600); err != nil {
		w.t.Fatal(err)
	}
	if !w.skipSecrets {
		path := filepath.Join(w.configDir, "secrets.env")
		if err := os.WriteFile(path, []byte(w.secrets), 0o600); err != nil {
			w.t.Fatal(err)
		}
		if err := os.Chmod(path, w.secretsPerm); err != nil {
			w.t.Fatal(err)
		}
	}
	return lightsoff.Run(args, lightsoff.Env{
		Stdout:     &w.stdout,
		Stderr:     &w.stderr,
		Stdin:      strings.NewReader(w.input),
		IsTerminal: func() bool { return w.terminal },
		StdoutIsTerminal: func() bool {
			if w.stdoutTerminal != nil {
				return *w.stdoutTerminal
			}
			return w.terminal
		},
		OpenURL: func(url string) error {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.openURLs = append(w.openURLs, url)
			return nil
		},
		Listen: w.listen,
		Interrupt: func() <-chan struct{} {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.catching = true
			return w.interrupt
		},
		HostMemory: func() (uint64, error) { return w.hostMemory, nil },
		Exec:       exec,
		Clock:      w,
		OnEvent: func(e lightsoff.Event) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.reportedEvents = append(w.reportedEvents, e)
		},
		ConfigDir: w.configDir,
		CacheDir:  w.cacheDir,
		StateDir:  w.stateDir,

		SbxConfigDir: w.sbxConfigDir,

		Version: w.version,
	})
}

// confirm attaches a terminal where the person answers yes to the --ticket prompt.
func (w *world) confirm() {
	w.terminal = true
	w.input = "y\n"
}

// openedURL returns the URL lights-off opened last, or "" if none.
func (w *world) openedURL() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.openURLs) == 0 {
		return ""
	}
	return w.openURLs[len(w.openURLs)-1]
}

// openedURLs returns every URL lights-off opened, in order.
func (w *world) openedURLs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.openURLs)
}

func (w *world) Now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *world) Sleep(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.now = w.now.Add(d)
}

func (w *world) exec(c lightsoff.Command) (lightsoff.Result, error) {
	w.mu.Lock()
	if w.interruptWhen != nil && w.interruptWhen(c) {
		w.pressCtrlC()
	}
	w.mu.Unlock()
	if c.Name == "sbx" && len(c.Args) > 3 && c.Args[0] == "exec" && slices.Contains(c.Args, "dsh") {
		defer w.agentRunning(c.Args[3])()
		if killed(c.Stop) {
			w.mu.Lock()
			defer w.mu.Unlock()
			w.commands = append(w.commands, c)
			return lightsoff.Result{Stdout: "partial", Interrupted: true}, nil
		}
	}
	w.mu.Lock()
	res, err := w.handle(c)
	holding := w.holdFor > 0
	w.mu.Unlock()
	if holding && c.Name == "git" && slices.Contains(c.Args, "checkout") {
		// Leave room for another ticket to move the checkout before this one
		// creates its sandbox, as it could if nothing kept them apart.
		time.Sleep(2 * time.Millisecond)
	}
	return res, err
}

// pressCtrlC is the person pressing Ctrl+C, once; w.mu is held. lights-off
// only hears it if it asked to catch it.
func (w *world) pressCtrlC() {
	if !w.interrupted {
		w.interrupted = true
		if w.catching {
			close(w.interrupt)
		}
	}
}

// killed reports whether stop has closed, which kills a command given it.
func killed(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return stop != nil
	default:
		return false
	}
}

// agentRunning counts an agent as running until the returned func is called,
// holding an Implementer as hold, holdFor and holdUntilMerged ask.
func (w *world) agentRunning(sandbox string) func() {
	w.mu.Lock()
	w.running++
	w.mostAtOnce = max(w.mostAtOnce, w.running)
	if w.interruptAtOnce > 0 && w.running >= w.interruptAtOnce {
		w.pressCtrlC()
	}
	firstOf42 := sandboxTicket(sandbox) == 42 && w.implementerRuns == 0
	w.mu.Unlock()
	if strings.Contains(sandbox, "-implementer-") {
		deadline := time.Now().Add(cmp.Or(w.holdFor, 2*time.Second))
		for time.Now().Before(deadline) {
			w.mu.Lock()
			_, merged := w.closesAt[w.holdUntilMerged]
			merged = merged || w.closed(w.holdUntilMerged)
			done := (w.hold == 0 || w.running >= w.hold) && (w.holdUntilMerged == 0 || !firstOf42 || merged)
			done = done || w.interrupted
			w.mu.Unlock()
			if done {
				break
			}
			time.Sleep(time.Millisecond)
		}
	}
	return func() {
		w.mu.Lock()
		w.running--
		w.mu.Unlock()
	}
}

func (w *world) closed(number int) bool {
	state, exists := w.stateOf(fmt.Sprintf("owner/repo#%d", number))
	return exists && !isOpenState(state)
}

func isOpenState(state string) bool { return strings.EqualFold(state, "open") }

func (w *world) handle(c lightsoff.Command) (lightsoff.Result, error) {
	w.commands = append(w.commands, c)
	w.closeMerged()
	switch c.Name {
	case "gh":
		return w.gh(c.Args), nil
	case "git":
		return w.git(c.Args), nil
	case "sbx":
		return w.sbx(c), nil
	}
	return lightsoff.Result{}, fmt.Errorf("unexpected command %q", c.Name)
}

func (w *world) gh(args []string) lightsoff.Result {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "issue view 42 --repo owner/repo"):
		return ok(fmt.Sprintf(`{"title":%q,"body":%q,"state":%q,"labels":%s}`, w.ticketTitle, w.ticketBody, w.ticketState, labelsJSON(w.ticketLabels)))
	case line == "api --paginate repos/owner/repo/issues/42/events":
		return w.events()
	case line == "api --paginate repos/owner/repo/issues/42/dependencies/blocked_by":
		return w.nativeBlockers()
	case len(args) == 2 && args[0] == "api" && issuePath.MatchString(args[1]):
		m := issuePath.FindStringSubmatch(args[1])
		state, exists := w.stateOf(m[1] + "#" + m[2])
		if !exists {
			return lightsoff.Result{Stdout: `{"message":"Not Found","status":"404"}`, Stderr: "gh: Not Found (HTTP 404)", ExitCode: 1}
		}
		return ok(fmt.Sprintf(`{"number":%s,"state":%q}`, m[2], strings.ToLower(state)))
	case strings.HasPrefix(line, "label list --repo owner/repo"):
		return ok(labelsJSON(w.repoLabels))
	case strings.HasPrefix(line, "label create ") && strings.HasSuffix(line, " --repo owner/repo"):
		w.repoLabels = append(w.repoLabels, args[2])
		return ok("")
	case strings.HasPrefix(line, "issue edit 42 --repo owner/repo"):
		return w.editLabels(args)
	case strings.HasPrefix(line, "repo view "):
		return ok(fmt.Sprintf(`{"defaultBranchRef":{"name":%q},"viewerDefaultMergeMethod":"SQUASH"}`, w.defaultBranch))
	case strings.HasPrefix(line, "repo clone"):
		if err := os.MkdirAll(filepath.Join(args[len(args)-1], ".git"), 0o755); err != nil {
			w.t.Fatal(err)
		}
		return ok("")
	case line == "api repos/owner/repo/contents/.github/workflows?ref=main":
		if w.workflowsExit != 0 {
			return lightsoff.Result{Stderr: "gh: Resource not accessible by integration (HTTP 403)", ExitCode: w.workflowsExit}
		}
		if len(w.workflowFiles) == 0 {
			return lightsoff.Result{Stdout: `{"message":"Not Found","status":"404"}`, Stderr: "gh: Not Found (HTTP 404)", ExitCode: 1}
		}
		var entries []string
		for _, name := range w.workflowFiles {
			entries = append(entries, fmt.Sprintf(`{"name":%q,"path":".github/workflows/%s","type":"file"}`, name, name))
		}
		return ok("[" + strings.Join(entries, ",") + "]")
	case line == "api repos/owner/repo/branches/main":
		if w.protectionHidden {
			return ok(`{"name":"main","protected":true}`)
		}
		if len(w.protectedChecks) == 0 {
			return ok(`{"name":"main","protected":false,"protection":{"enabled":false,"required_status_checks":{"enforcement_level":"off","contexts":[],"checks":[]}}}`)
		}
		var contexts, checks []string
		for _, name := range w.protectedChecks {
			contexts = append(contexts, fmt.Sprintf("%q", name))
			checks = append(checks, fmt.Sprintf(`{"context":%q,"app_id":null}`, name))
		}
		return ok(fmt.Sprintf(`{"name":"main","protected":true,"protection":{"enabled":true,"required_status_checks":{"enforcement_level":"non_admins","contexts":[%s],"checks":[%s]}}}`,
			strings.Join(contexts, ","), strings.Join(checks, ",")))
	case line == "api --paginate repos/owner/repo/rules/branches/main --jq .[].type":
		switch w.rulesExit {
		case "plan": // what a real private repository on GitHub Free answers
			return lightsoff.Result{
				Stdout:   `{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature.","status":"403"}`,
				Stderr:   "gh: Upgrade to GitHub Pro or make this repository public to enable this feature. (HTTP 403)",
				ExitCode: 1,
			}
		case "permission":
			return lightsoff.Result{Stderr: "gh: Resource not accessible by personal access token (HTTP 403)", ExitCode: 1}
		}
		// Like gh --paginate --jq, one line per rule across every page; a ruleset's
		// required checks come after enough other rules to need a second page.
		types := slices.Repeat([]string{"deletion"}, 30)
		for range w.rulesetChecks {
			types = append(types, "required_status_checks")
		}
		return ok(strings.Join(types, "\n") + "\n")
	case strings.HasPrefix(line, "pr list --repo owner/repo"):
		// Like gh, only open pull requests, and only #42's: other tickets'
		// Implementers are never stopped before their answer names theirs.
		if flagValue(args, "--state") != "open" {
			w.t.Errorf("pr list without --state open: %v", args)
		}
		if (w.implementerRuns > 0 || w.prOpenedByKilledPass) && w.state() == "OPEN" {
			return ok(fmt.Sprintf(`[{"number":7,"body":%q}]`, w.prBody))
		}
		return ok("[]")
	case strings.HasPrefix(line, "pr view 7 --repo owner/repo"):
		checks := w.checks[0]
		if len(w.checks) > 1 {
			w.checks = w.checks[1:]
		}
		return ok(fmt.Sprintf(`{"state":%q,"body":%q,"baseRefName":%q,"headRefOid":%q,"mergeable":%q,"statusCheckRollup":%s}`,
			w.state(), w.prBody, w.prBase, w.head(), w.mergeable(), checks))
	case strings.HasPrefix(line, "pr edit 7 --repo owner/repo"):
		if w.prLabelExit != 0 {
			return lightsoff.Result{Stderr: "HTTP 403: Resource not accessible", ExitCode: w.prLabelExit}
		}
		name := flagValue(args, "--add-label")
		if !w.labelDefined(name) {
			return lightsoff.Result{Stderr: fmt.Sprintf("'%s' not found", name), ExitCode: 1}
		}
		w.prLabels = append(w.prLabels, name)
		return ok("")
	case strings.HasPrefix(line, "pr comment 7 --repo owner/repo"):
		return lightsoff.Result{ExitCode: w.commentExit}
	case strings.HasPrefix(line, "pr merge 7 --repo owner/repo"):
		if w.conflictAtMerge {
			w.conflictAtMerge = false
			w.conflictedAtHead = true
			w.unknownReads = w.unknownAfterMerge
			return lightsoff.Result{Stderr: "Pull request is not mergeable: the merge commit cannot be cleanly created", ExitCode: 1}
		}
		if w.mergeExit == 0 {
			w.closesAt[42] = w.now.Add(w.closeDelay) // Closes #42
		}
		return lightsoff.Result{ExitCode: w.mergeExit}
	}
	if res, handled := w.ghOther(args); handled {
		return res
	}
	w.t.Errorf("unscripted gh command: %s", line)
	return lightsoff.Result{ExitCode: 1}
}

// editLabels is gh issue edit: like the real gh, a label the repository doesn't
// define fails the command, whether it's being added or removed.
func (w *world) editLabels(args []string) lightsoff.Result {
	if w.labelEditExit != 0 {
		return lightsoff.Result{Stderr: "HTTP 403: Resource not accessible", ExitCode: w.labelEditExit}
	}
	add := strings.Split(flagValue(args, "--add-label"), ",")
	remove := strings.Split(flagValue(args, "--remove-label"), ",")
	for _, name := range slices.Concat(add, remove) {
		if name != "" && !w.labelDefined(name) {
			return lightsoff.Result{Stderr: fmt.Sprintf("'%s' not found", name), ExitCode: 1}
		}
	}
	w.ticketLabels = slices.DeleteFunc(w.ticketLabels, func(name string) bool { return slices.Contains(remove, name) })
	w.ticketLabels = append(w.ticketLabels, add...)
	return ok("")
}

func (w *world) labelDefined(name string) bool {
	return slices.ContainsFunc(w.repoLabels, func(l string) bool { return strings.EqualFold(l, name) })
}

func (w *world) git(args []string) lightsoff.Result {
	if slices.Contains(args, "merge-base") {
		return ok(mergeBase + "\n")
	}
	if i := slices.Index(args, "checkout"); i >= 0 {
		w.checkoutHead = args[len(args)-1]
	}
	if i := slices.IndexFunc(args, func(arg string) bool {
		return strings.HasPrefix(arg, "--glob=refs/lights-off/sandboxes/")
	}); i >= 0 {
		// Like git log on the branches fetched from one sandbox's remote, with
		// -z between path names.
		sandbox := strings.TrimSuffix(strings.TrimPrefix(args[i], "--glob=refs/lights-off/sandboxes/"), "/*")
		if sandboxTicket(sandbox) != 42 {
			return ok("")
		}
		return ok(strings.Join(w.implementerFilesForPass(), "\x00"))
	}
	return ok("")
}

// implementerFilesForPass is what the latest Implementer pass of #42 left.
func (w *world) implementerFilesForPass() []string {
	if w.implementerRuns > 0 && w.implementerRuns <= len(w.implementerFiles) {
		return w.implementerFiles[w.implementerRuns-1]
	}
	return nil
}

func (w *world) sbx(c lightsoff.Command) lightsoff.Result {
	args := c.Args
	switch args[0] {
	case "ls":
		if slices.Contains(args, "--json") {
			return ok(w.listing)
		}
		var names []string
		for _, name := range w.sandboxes {
			if !w.gone[name] {
				names = append(names, name)
			}
		}
		return ok(strings.Join(names, "\n"))
	case "create":
		// The agent is sbx's built-in claude or a local kit.
		kit := args[len(args)-2]
		if _, err := os.Stat(filepath.Join(kit, "spec.yaml")); kit != "claude" && err != nil {
			w.t.Errorf("kit %q has no spec.yaml: %v", kit, err)
		}
		if len(w.sandboxes) == 0 {
			// Without a binding the real sbx withholds the credential.
			data, _ := os.ReadFile(filepath.Join(w.sbxConfigDir, "credentials.yaml"))
			w.bindingsAtCreate = string(data)
		}
		w.sandboxes = append(w.sandboxes, flagValue(args, "--name"))
		w.clonedFrom[flagValue(args, "--name")] = w.checkoutHead
		return ok("")
	case "exec":
		// Like the real sbx, an unknown sandbox exits 125.
		i := slices.Index(w.sandboxes, args[3])
		if i < 0 || w.gone[args[3]] {
			return lightsoff.Result{Stderr: "no such sandbox", ExitCode: 125}
		}
		// Like the real sbx, exec starts a stopped sandbox again.
		delete(w.stopped, args[3])
		if !slices.Contains(args, "dsh") {
			return ok("")
		}
		for _, other := range w.sandboxes {
			if other != args[3] && sandboxTicket(other) == sandboxTicket(args[3]) && !w.gone[other] && !w.stopped[other] {
				w.overlaps = append(w.overlaps, other)
			}
		}
		if number := sandboxTicket(args[3]); number != 42 {
			return w.otherAgent(number, args[3])
		}
		if strings.Contains(args[3], "-implementer-") {
			w.implementerRuns++
			w.conflictedAtHead = false
			return w.agentAnswer(c, w.implementerRuns, w.implementerPasses, w.implementerOutput, w.implementerExit)
		}
		w.reviewerRuns++
		return w.agentAnswer(c, w.reviewerRuns, w.reviewerPasses, w.reviewerOutput, w.reviewerExit)
	case "stop":
		// A sandbox that isn't running is left as it is, but asking twice is a bug
		// in the lifecycle, and the real command's answer to it isn't known.
		if w.stopped[args[1]] {
			w.t.Errorf("sbx stop on stopped sandbox %q", args[1])
		}
		w.stopped[args[1]] = true
		return lightsoff.Result{ExitCode: w.stopExit}
	case "rm":
		// Like the real sbx, removal asks for confirmation, which an unattended
		// run can't answer, unless --force is passed.
		if !slices.Contains(args, "--force") {
			return lightsoff.Result{Stderr: "Remove sandbox? This cannot be undone. (y/N)", ExitCode: 1}
		}
		name := args[len(args)-1]
		if !w.rmLeavesSandbox {
			w.gone[name] = true
		}
		if w.rmExit != 0 {
			if w.rmCleansDeepseek {
				delete(w.scoped[name], "deepseek")
			}
			return lightsoff.Result{Stderr: "delete credential: Keychain Error. (100001)", ExitCode: w.rmExit}
		}
		delete(w.scoped, name)
		return ok("")
	case "secret":
		return w.sbxSecret(c)
	case "policy":
		return ok("")
	case "run":
		// Like the real sbx, run attaches to a sandbox that exists.
		name := flagValue(args, "--name")
		if !slices.Contains(w.sandboxes, name) || w.gone[name] {
			return lightsoff.Result{ExitCode: 125}
		}
		return lightsoff.Result{ExitCode: w.sessionExit}
	}
	w.t.Errorf("unscripted sbx command: %v", args)
	return lightsoff.Result{ExitCode: 125}
}

func (w *world) sbxSecret(c lightsoff.Command) lightsoff.Result {
	args := c.Args
	sandbox := flagValue(args, "--sandbox")
	if slices.Contains(args, "--help") {
		if w.noValueStdin {
			return ok("Flags:\n  --env string\n  --host stringArray\n")
		}
		return ok("Flags:\n  --env string\n  --host stringArray\n  --value-stdin\n")
	}
	switch args[1] {
	case "set", "set-custom":
		if w.secretSetExit != 0 {
			// A failure message can quote what it was given.
			return lightsoff.Result{Stderr: "rejected value " + c.Stdin, ExitCode: w.secretSetExit}
		}
		name := args[2]
		if args[1] == "set-custom" {
			name = flagValue(args, "--placeholder")
		}
		// Like the real sbx, an existing secret isn't silently overwritten.
		if _, exists := w.scoped[sandbox][name]; exists {
			return lightsoff.Result{Stderr: "secret already exists. Overwrite? (y/N)", ExitCode: 1}
		}
		if w.scoped[sandbox] == nil {
			w.scoped[sandbox] = map[string]string{}
		}
		w.scoped[sandbox][name] = c.Stdin
		return ok("")
	case "rm":
		// Like sbx rm, this confirms unless --force is passed.
		if !slices.Contains(args, "--force") {
			return lightsoff.Result{Stderr: "Delete secret? (y/N)", ExitCode: 1}
		}
		if w.secretRmExit != 0 {
			return lightsoff.Result{Stderr: "delete credential: Keychain Error. (100001)", ExitCode: w.secretRmExit}
		}
		name := args[2]
		if p := flagValue(args, "--placeholder"); p != "" {
			name = p
		}
		if _, stored := w.scoped[sandbox][name]; !stored {
			return lightsoff.Result{Stderr: "secret not found", ExitCode: 1}
		}
		delete(w.scoped[sandbox], name)
		return ok("")
	case "ls":
		var names []string
		for name := range w.scoped[sandbox] {
			names = append(names, name)
		}
		return ok(strings.Join(names, "\n"))
	}
	w.t.Errorf("unscripted sbx command: %v", args)
	return lightsoff.Result{ExitCode: 125}
}

// agentPass is one scripted dsh run. A hung pass runs until its limit stops it.
type agentPass struct {
	output string
	exit   int
	stderr string
	hangs  bool
}

func (w *world) agentAnswer(c lightsoff.Command, run int, passes []agentPass, output string, exit int) lightsoff.Result {
	pass := agentPass{output: output, exit: exit}
	if run <= len(passes) {
		pass = passes[run-1]
	}
	if pass.hangs {
		w.now = w.now.Add(c.Timeout)
		return lightsoff.Result{Stdout: "partial", TimedOut: true}
	}
	return lightsoff.Result{Stdout: pass.output, Stderr: pass.stderr, ExitCode: pass.exit}
}

func (w *world) head() string {
	if w.implementerRuns == 0 {
		return w.heads[0]
	}
	return w.heads[min(w.implementerRuns, len(w.heads))-1]
}

func (w *world) state() string {
	if w.closedFromPass > 0 && w.implementerRuns >= w.closedFromPass {
		return "CLOSED"
	}
	return w.prState
}

func (w *world) mergeable() string {
	if w.unknownReads > 0 {
		w.unknownReads--
		return "UNKNOWN"
	}
	if w.conflictedAtHead || w.implementerRuns <= w.conflictedPasses {
		return "CONFLICTING"
	}
	return "MERGEABLE"
}

// labelEvent is one entry of a ticket's label history.
type labelEvent struct {
	event string // labeled or unlabeled
	label string
	actor string
}

// events is the issue events API: like gh --paginate, a page per JSON array,
// with other kinds of event mixed in.
func (w *world) events() lightsoff.Result {
	if w.eventsExit != 0 {
		return lightsoff.Result{Stderr: "gh: Server Error (HTTP 502)", ExitCode: w.eventsExit}
	}
	pages := []string{`[{"event":"referenced","actor":{"login":"someone"}}]`}
	for _, e := range w.labelEvents {
		pages = append(pages, fmt.Sprintf(`[{"event":%q,"actor":{"login":%q},"label":{"name":%q,"color":"ededed"}}]`, e.event, e.actor, e.label))
	}
	return ok(strings.Join(pages, ""))
}

func (w *world) nativeBlockers() lightsoff.Result {
	if w.blockedExit != 0 {
		return lightsoff.Result{Stderr: "gh: Not Found (HTTP 404)", ExitCode: w.blockedExit}
	}
	var entries []string
	for _, ref := range w.blockedBy {
		repo, number, _ := strings.Cut(ref, "#")
		state, _ := w.stateOf(ref)
		state = strings.ToLower(state)
		if state == "" {
			state = "open"
		}
		entries = append(entries, fmt.Sprintf(`{"number":%s,"state":%q,"html_url":"https://github.com/%s/issues/%s"}`, number, state, repo, number))
	}
	return ok("[" + strings.Join(entries, ",") + "]")
}

func labelsJSON(names []string) string {
	var labels []string
	for _, name := range names {
		labels = append(labels, fmt.Sprintf(`{"name":%q}`, name))
	}
	return "[" + strings.Join(labels, ",") + "]"
}

func ok(stdout string) lightsoff.Result { return lightsoff.Result{Stdout: stdout} }

func flagValue(args []string, name string) string {
	if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// calls returns the commands whose name and arguments start with prefix.
func (w *world) calls(prefix ...string) []lightsoff.Command {
	var found []lightsoff.Command
	for _, c := range w.commands {
		full := append([]string{c.Name}, c.Args...)
		if len(full) >= len(prefix) && slices.Equal(full[:len(prefix)], prefix) {
			found = append(found, c)
		}
	}
	return found
}

// indexOf returns the position of the first command containing all of words.
func (w *world) indexOf(words ...string) int {
	return slices.IndexFunc(w.commands, func(c lightsoff.Command) bool {
		for _, word := range words {
			if c.Name != word && !slices.Contains(c.Args, word) {
				return false
			}
		}
		return true
	})
}

func (w *world) prompt(sandbox string) string {
	w.t.Helper()
	prompts := w.prompts(sandbox)
	if len(prompts) == 0 {
		w.t.Fatalf("no dsh run in %q", sandbox)
	}
	return prompts[0]
}

// prompts returns what each dsh run in a sandbox was told, in order.
func (w *world) prompts(sandbox string) []string {
	var found []string
	for _, c := range w.calls("sbx", "exec") {
		if slices.Contains(c.Args, sandbox) && slices.Contains(c.Args, "dsh") {
			found = append(found, c.Args[len(c.Args)-1])
		}
	}
	return found
}

func (w *world) assertNoMerge() {
	w.t.Helper()
	if got := w.calls("gh", "pr", "merge"); len(got) != 0 {
		w.t.Errorf("merge command issued: %v\nstdout:\n%s", got, w.stdout.String())
	}
}

func (w *world) assertNotMovedToReadyForHuman() {
	w.t.Helper()
	if i := w.indexOf("gh", "issue", "edit", "ready-for-human"); i >= 0 {
		w.t.Errorf("ticket moved to ready-for-human: %v", w.commands[i])
	}
}

// assertNotStarted checks that lights-off refused before claiming the ticket or
// creating a sandbox.
func (w *world) assertNotStarted(code int) {
	w.t.Helper()
	if code == 0 {
		w.t.Errorf("exit code = 0, want non-zero")
	}
	if got := w.calls("sbx", "create"); len(got) != 0 {
		w.t.Errorf("sandbox created: %v", got)
	}
	if got := w.calls("gh", "issue", "edit"); len(got) != 0 {
		w.t.Errorf("ticket labels changed: %v", got)
	}
}

func (w *world) assertStderrHas(parts ...string) {
	w.t.Helper()
	for _, part := range parts {
		if !strings.Contains(w.stderr.String(), part) {
			w.t.Errorf("stderr lacks %q:\n%s", part, w.stderr.String())
		}
	}
}

func (w *world) assertSandboxesKept() {
	w.t.Helper()
	if got := w.calls("sbx", "rm"); len(got) != 0 {
		w.t.Errorf("sandbox removed: %v", got)
	}
	for _, name := range w.sandboxes {
		if !strings.Contains(w.stdout.String(), name) {
			w.t.Errorf("kept sandbox %q not named in output:\n%s", name, w.stdout.String())
		}
	}
}

// assertWaited checks that about want passed on the clock since start.
func (w *world) assertWaited(start time.Time, want time.Duration) {
	w.t.Helper()
	if waited := w.now.Sub(start); waited < want || waited > want+time.Minute {
		w.t.Errorf("waited %v, want about %v", waited, want)
	}
}

// assertEventOnce checks that exactly one status line matches pattern.
func (w *world) assertEventOnce(pattern string) {
	w.t.Helper()
	re := regexp.MustCompile(pattern)
	count := 0
	for line := range strings.SplitSeq(w.stdout.String(), "\n") {
		if re.MatchString(line) {
			count++
		}
	}
	if count != 1 {
		w.t.Errorf("want one status line matching %q, got %d in:\n%s", pattern, count, w.stdout.String())
	}
}

// eventsOf returns every structured event of one kind, in the order reported.
func (w *world) eventsOf(kind lightsoff.EventKind) []lightsoff.Event {
	w.t.Helper()
	var found []lightsoff.Event
	for _, e := range w.reportedEvents {
		if e.Kind == kind {
			found = append(found, e)
		}
	}
	return found
}

// eventFor returns the last structured event of one kind for a ticket.
func (w *world) eventFor(kind lightsoff.EventKind, ticket int) (lightsoff.Event, bool) {
	w.t.Helper()
	for i := len(w.reportedEvents) - 1; i >= 0; i-- {
		if e := w.reportedEvents[i]; e.Kind == kind && e.Ticket == ticket {
			return e, true
		}
	}
	return lightsoff.Event{}, false
}

// issuePath is the REST path of one issue or pull request.
var issuePath = regexp.MustCompile(`^repos/([^/]+/[^/]+)/issues/(\d+)$`)

var errNotInstalled = errors.New("executable file not found")

// otherTicket is a ticket besides #42. Its Implementer opens pull request
// 100+number, which CI passes, and its Reviewer answers verdict. Merging that
// pull request closes the ticket, as Closes #N would.
type otherTicket struct {
	opened    time.Time
	body      string
	state     string
	labels    []string
	labeledBy string   // who applied ready-for-agent
	blockedBy []string // native blockers, as owner/repo#N
	verdict   string
}

// addTicket adds an open ticket that passes every check, opened at opened.
func (w *world) addTicket(number int, opened time.Time) *otherTicket {
	t := &otherTicket{opened: opened, body: fmt.Sprintf("Do %d.", number), state: "OPEN", labels: []string{"ready-for-agent"}, labeledBy: "owner", verdict: mergeVerdict}
	w.others[number] = t
	return t
}

// stateOf is an issue's state, by owner/repo#N, and whether it exists.
func (w *world) stateOf(ref string) (string, bool) {
	if ref == "owner/repo#42" {
		return w.ticketState, true
	}
	if repo, number, _ := strings.Cut(ref, "#"); repo == "owner/repo" {
		if t, found := w.others[atoi(number)]; found {
			return t.state, true
		}
	}
	state, exists := w.issueStates[ref]
	return state, exists
}

var (
	otherIssueCmd = regexp.MustCompile(`^issue (view|edit) (\d+) --repo owner/repo`)
	otherEvents   = regexp.MustCompile(`^api --paginate repos/owner/repo/issues/(\d+)/events$`)
	otherBlockers = regexp.MustCompile(`^api --paginate repos/owner/repo/issues/(\d+)/dependencies/blocked_by$`)
	otherPRCmd    = regexp.MustCompile(`^pr (view|comment|edit|merge) (\d+) --repo owner/repo`)
	sandboxOf     = regexp.MustCompile(`-(\d+)-(?:implementer|reviewer)-\d+-\d+$`)
)

// ghOther answers gh for the other tickets, their pull requests, and the list
// of open tickets.
func (w *world) ghOther(args []string) (lightsoff.Result, bool) {
	line := strings.Join(args, " ")
	if strings.HasPrefix(line, "issue list --repo owner/repo") {
		return w.issueList(args), true
	}
	if m := otherIssueCmd.FindStringSubmatch(line); m != nil {
		t, found := w.others[atoi(m[2])]
		if !found {
			return lightsoff.Result{Stderr: "GraphQL: Could not resolve to an issue", ExitCode: 1}, true
		}
		if m[1] == "view" {
			return ok(fmt.Sprintf(`{"title":"Ticket %s","body":%q,"state":%q,"labels":%s}`, m[2], t.body, t.state, labelsJSON(t.labels))), true
		}
		add := strings.Split(flagValue(args, "--add-label"), ",")
		remove := strings.Split(flagValue(args, "--remove-label"), ",")
		t.labels = append(slices.DeleteFunc(t.labels, func(name string) bool { return slices.Contains(remove, name) }), add...)
		return ok(""), true
	}
	if m := otherEvents.FindStringSubmatch(line); m != nil {
		if t, found := w.others[atoi(m[1])]; found {
			return ok(fmt.Sprintf(`[{"event":"labeled","actor":{"login":%q},"label":{"name":"ready-for-agent"}}]`, t.labeledBy)), true
		}
	}
	if m := otherBlockers.FindStringSubmatch(line); m != nil {
		if t, found := w.others[atoi(m[1])]; found {
			var entries []string
			for _, ref := range t.blockedBy {
				repo, number, _ := strings.Cut(ref, "#")
				state, _ := w.stateOf(ref)
				entries = append(entries, fmt.Sprintf(`{"number":%s,"state":%q,"html_url":"https://github.com/%s/issues/%s"}`,
					number, strings.ToLower(cmp.Or(state, "open")), repo, number))
			}
			return ok("[" + strings.Join(entries, ",") + "]"), true
		}
	}
	if m := otherPRCmd.FindStringSubmatch(line); m != nil {
		number := atoi(m[2]) - 100
		if _, found := w.others[number]; !found {
			return lightsoff.Result{}, false
		}
		switch m[1] {
		case "view":
			return ok(fmt.Sprintf(`{"state":"OPEN","body":"Closes #%d\n","baseRefName":"main","headRefOid":%q,"mergeable":"MERGEABLE","statusCheckRollup":%s}`,
				number, otherHead(number), greenChecks)), true
		case "merge":
			w.closesAt[number] = w.now.Add(w.closeDelay)
			if number == w.conflictWhenMerged {
				// #42's first pass is still running, so what it pushes is on
				// the old base; its next pass resolves the conflict.
				w.conflictedPasses = max(w.conflictedPasses, 1)
			}
		}
		return ok(""), true
	}
	return lightsoff.Result{}, false
}

// issueList is gh issue list: open tickets, carrying the label asked for when
// there is one, newest first unless the search sorts them otherwise, and no more
// than --limit. The graph asks without a label, so it gets every open ticket.
func (w *world) issueList(args []string) lightsoff.Result {
	if flagValue(args, "--state") != "open" {
		w.t.Errorf("issue list without --state open: %v", args)
	}
	label := flagValue(args, "--label")
	if label != "" && label != "ready-for-agent" {
		w.t.Errorf("issue list with an unexpected label: %v", args)
	}
	type listed struct {
		number int
		title  string
		body   string
		labels []string
		opened time.Time
	}
	var found []listed
	if w.ticketState == "OPEN" && (label == "" || slices.Contains(w.ticketLabels, "ready-for-agent")) {
		found = append(found, listed{42, w.ticketTitle, w.ticketBody, w.ticketLabels, w.ticketOpened})
	}
	for number, t := range w.others {
		if t.state == "OPEN" && (label == "" || slices.Contains(t.labels, "ready-for-agent")) {
			found = append(found, listed{number, fmt.Sprintf("Ticket %d", number), t.body, t.labels, t.opened})
		}
	}
	slices.SortFunc(found, func(a, b listed) int { return b.opened.Compare(a.opened) })
	if strings.Contains(flagValue(args, "--search"), "sort:created-asc") {
		slices.Reverse(found)
	}
	if limit := atoi(flagValue(args, "--limit")); limit > 0 && len(found) > limit {
		found = found[:limit]
	}
	var entries []string
	for _, l := range found {
		entries = append(entries, fmt.Sprintf(`{"number":%d,"title":%q,"body":%q,"state":"OPEN","labels":%s,"url":%q,"createdAt":%q}`,
			l.number, l.title, l.body, labelsJSON(l.labels), fmt.Sprintf("https://github.com/owner/repo/issues/%d", l.number), l.opened.Format(time.RFC3339)))
	}
	return ok("[" + strings.Join(entries, ",") + "]")
}

// otherAgent answers dsh in another ticket's sandbox.
func (w *world) otherAgent(number int, sandbox string) lightsoff.Result {
	t, found := w.others[number]
	if !found {
		w.t.Errorf("agent run for unknown ticket #%d", number)
		return lightsoff.Result{ExitCode: 1}
	}
	if strings.Contains(sandbox, "-implementer-") {
		return ok(fmt.Sprintf(`{"status":"finished","pr":%d,"reason":"done"}`, 100+number))
	}
	return ok(t.verdict)
}

// otherHead is the head commit of another ticket's pull request.
func otherHead(number int) string { return fmt.Sprintf("cccc%04d", number) }

func sandboxTicket(name string) int {
	m := sandboxOf.FindStringSubmatch(name)
	if m == nil {
		return 42
	}
	return atoi(m[1])
}

func atoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}

// claimed returns the tickets claimed, in the order they were claimed.
func (w *world) claimed() []int {
	var numbers []int
	for _, c := range w.calls("gh", "issue", "edit") {
		if flagValue(c.Args, "--add-label") == "agent-in-progress" {
			numbers = append(numbers, atoi(c.Args[2]))
		}
	}
	return numbers
}

// closeMerged closes each merged ticket once its closeDelay has passed, as
// GitHub does a moment after the merge.
func (w *world) closeMerged() {
	for number, at := range w.closesAt {
		if w.now.Before(at) {
			continue
		}
		delete(w.closesAt, number)
		if number == 42 {
			w.ticketState = "CLOSED"
		} else if t, found := w.others[number]; found {
			t.state = "CLOSED"
		}
	}
}
