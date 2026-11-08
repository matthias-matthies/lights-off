package lightsoff

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/matthias-matthies/lights-off/kits"
)

const (
	ciPollInterval = 30 * time.Second
	ciTimeout      = 30 * time.Minute
	// noCIWait is how long a pull request must report zero checks before the
	// no-CI rule can apply, because checks take a moment to appear.
	noCIWait = 2 * time.Minute

	mergeablePollInterval = 5 * time.Second
	mergeableTimeout      = time.Minute

	// GitHub closes a ticket a moment after the merge that closes it.
	closePollInterval = 5 * time.Second
	closeTimeout      = time.Minute
)

// gateDecision is what the Merge Gate makes of a pull request. The zero value
// never merges.
type gateDecision int

const (
	gateHandBack gateDecision = iota
	gateReadyForHuman
	gateMerge
)

// mergeGate is the rule that decides a merge, and the only caller of merge
// depends on it. It sees only CI and the Verdict: nothing the Implementer wrote
// on the pull request can change the outcome.
func mergeGate(ci ciState, v Verdict) gateDecision {
	switch {
	case (ci != ciGreen && ci != ciAbsent) || !v.Mergeable:
		return gateHandBack
	case v.Risk == "low" && v.Update == "two-way":
		return gateMerge
	}
	return gateReadyForHuman
}

// maxIterations is how many Iterations one Ticket Attempt may use.
const maxIterations = 3

// handBack ends an Iteration with something the Implementer can act on. Any
// other error means lights-off itself failed.
type handBack struct{ reason string }

func (h handBack) Error() string { return h.reason }

func crashed(agent, detail string) handBack {
	return handBack{"the " + agent + " crashed: " + detail}
}

// attempt is one Ticket Attempt.
type attempt struct {
	env     Env
	repo    string
	number  int
	report  attemptReporter
	sh      shell // stops at Ctrl+C
	cleanup shell // for what Ctrl+C itself calls for
	values  secrets
	config  repoConfig

	logDir       string
	stop         <-chan struct{}
	hostCheckout *sync.Mutex // see autonomousRun

	ticket   ticket
	info     repoInfo
	checkout checkout
	driver   sandboxes

	sandboxes   []*sandbox // created so far, in order
	implementer *sandbox   // kept across Iterations
	pr          int        // the pull request for this ticket, once one has checked out
	handedBack  string     // why the last Iteration went back to the Implementer
}

func kitDir(cacheDir string) string { return filepath.Join(cacheDir, "kits", "dsh") }

func newAttempt(repo string, t ticket, config repoConfig, values secrets, run autonomousRun, env Env, report *reporter) *attempt {
	return &attempt{
		env:          env,
		repo:         repo,
		number:       t.Number,
		ticket:       t,
		report:       attemptReporter{out: report, ticket: t.Number, phase: PhaseQueued},
		sh:           shell{exec: stoppable(env.Exec, run.stop)},
		cleanup:      shell{exec: env.Exec},
		values:       values,
		config:       config,
		logDir:       run.logDir,
		stop:         run.stop,
		hostCheckout: run.hostCheckout,
	}
}

// claim takes the ticket for this run. It is apart from the rest so that a
// scheduler starting several tickets claims them in the order it picked them.
// Unless it is claimed, the outcome is how it ended.
func (a *attempt) claim() (outcome, bool) {
	// A selected ticket is queued before its first Iteration, so that a
	// subscriber that is already listening sees where it started. Queued has no
	// status line, so the terminal only gains the claimed line next.
	a.report.setPhase(PhaseQueued)
	a.report.setIteration(1)
	a.report.emit(Event{Kind: EventQueued})
	err := a.sh.claim(a.repo, a.number)
	if errors.Is(err, errInterrupted) {
		// The only change a claim makes is its last, so nothing was changed.
		return outcome{}, false
	}
	if err != nil {
		a.report.emit(Event{Kind: EventStopped, Reason: err.Error()})
		return outcome{ticket: a.number, code: exitFailure, needs: fmt.Sprintf("stopped while claiming it: %v", err)}, false
	}
	a.report.setPhase(PhaseImplementing)
	a.report.setIteration(1)
	a.report.emit(Event{Kind: EventClaimed})
	return outcome{}, true
}

// run takes a claimed ticket to its end and reports how it ended.
func (a *attempt) run() outcome {
	end, err := a.runToEnding()
	if errors.Is(err, errInterrupted) {
		return a.interrupted()
	}
	if err != nil {
		a.report.emit(Event{Kind: EventStopped, Reason: err.Error()})
		return a.keep(exitFailure, fmt.Sprintf("stopped: %v", err))
	}
	if end.readyForHuman {
		a.report.setPhase(PhaseReadyForHuman)
		// An expected outcome, so it doesn't look like an error to a scheduler.
		if end.pr == 0 {
			a.report.clearPullRequest()
			a.report.emit(Event{Kind: EventReadyForHuman, Reason: end.reason})
			return a.keep(exitOK, "no pull request: "+end.reason)
		}
		a.report.emit(Event{Kind: EventReadyForHuman, PullRequest: end.pr, Reason: end.reason})
		return a.keep(exitOK, fmt.Sprintf("pull request #%d open: %s", end.pr, end.reason))
	}
	a.report.setPhase(PhaseMerged)
	a.report.emit(Event{Kind: EventMerged, PullRequest: end.pr})
	merged := outcome{ticket: a.number, merged: true, pr: end.pr}
	if err := a.waitForClose(); err != nil {
		a.report.emit(Event{Kind: EventStopped, Reason: err.Error()})
		kept := a.keep(exitFailure, fmt.Sprintf("stopped after the merge: %v", err))
		kept.merged, kept.pr = true, end.pr
		return kept
	}
	if err := a.removeSandboxes(); err != nil {
		merged.code, merged.needs = exitFailure, "sandbox removal failed: "+err.Error()
	}
	return merged
}

// keep ends the attempt with its sandboxes kept for a person, naming each.
func (a *attempt) keep(code int, needs string) outcome {
	o := outcome{ticket: a.number, code: code, pr: a.pr, needs: needs}
	for _, sb := range a.sandboxes {
		a.report.emit(Event{Kind: EventSandboxKept, Sandbox: sb.name})
		o.kept = append(o.kept, sb.name)
	}
	return o
}

// waitForClose waits for GitHub to close the ticket after its merge, because
// only then are the tickets it blocks free to start, possibly later in this run.
// A ticket still open at the limit is no failure: the merge did happen.
func (a *attempt) waitForClose() error {
	deadline := a.env.Clock.Now().Add(closeTimeout)
	for {
		state, err := a.sh.issueState(issueRef{repo: a.repo, number: a.number})
		if errors.Is(err, errInterrupted) {
			return nil // the merge happened, and nothing else waits on this
		}
		if err != nil {
			return err
		}
		if !isOpen(state) {
			return nil
		}
		if !a.env.Clock.Now().Before(deadline) {
			a.report.emit(Event{Kind: EventStillOpenAfterMerge, Reason: closeTimeout.String()})
			return nil
		}
		a.env.Clock.Sleep(closePollInterval)
	}
}

// ending is how a Ticket Attempt finished without anything going wrong.
type ending struct {
	pr            int
	readyForHuman bool
	reason        string
}

func (a *attempt) runToEnding() (ending, error) {
	var err error
	if a.info, err = a.sh.readRepo(a.repo); err != nil {
		return ending{}, err
	}
	if err := a.openCheckout(); err != nil {
		return ending{}, err
	}

	for iteration := 1; iteration <= maxIterations; iteration++ {
		end, err := a.runIteration(iteration)
		var back handBack
		if !errors.As(err, &back) {
			return end, err
		}
		a.handedBack = back.reason
		if iteration == maxIterations {
			// No next Iteration is left to enter, so the attempt ends at a
			// person rather than returning to Implementing.
			a.report.setPhase(PhaseReadyForHuman)
			a.report.setIteration(iteration)
		} else {
			// The Hand-back returns the attempt to Implementing for the next
			// Iteration, so the event carries that one.
			a.report.setPhase(PhaseImplementing)
			a.report.setIteration(iteration + 1)
		}
		a.report.emit(Event{Kind: EventHandedBack, Reason: back.reason})
	}
	// No next Iteration is left to enter.
	a.report.setIteration(maxIterations)
	return a.stopAtPerson(fmt.Sprintf("%d iterations used, the last handed back: %s", maxIterations, a.handedBack))
}

// openCheckout clones the host checkout if this is the first ticket to need it,
// and writes the kit, which every sandbox of the run reads.
func (a *attempt) openCheckout() error {
	a.hostCheckout.Lock()
	defer a.hostCheckout.Unlock()
	var err error
	if a.checkout, err = openCheckout(a.sh, a.env.CacheDir, a.repo); err != nil {
		return err
	}
	kit := kitDir(a.env.CacheDir)
	if err := kits.WriteDsh(kit); err != nil {
		return err
	}
	a.driver = sandboxes{shell: a.sh, kit: kit, workspace: a.checkout.dir, ctrlC: a.stop}
	return nil
}

// runIteration is one Implementer pass and the outcome that decides what happens
// next. A hand-back comes back as an error, so that the rest is the straight path.
func (a *attempt) runIteration(iteration int) (ending, error) {
	a.report.setPhase(PhaseImplementing)
	a.report.setIteration(iteration)
	base := a.info.DefaultBranchRef.Name
	result, runErr := a.runImplementer(iteration)
	// A workflow change is judged from the work, not the Implementer's answer,
	// because the refused push may make it report a crash or a block instead.
	// A Tool failure says nothing about the work, so it is left to fail normally.
	var back handBack
	if runErr == nil || errors.As(runErr, &back) {
		files, inspectErr := a.changedWorkflowFiles(base)
		if len(files) > 0 {
			return a.stopAtPerson(workflowChangeReason(files))
		}
		// A crash keeps its own outcome if the work can't be read, because it
		// may have stopped the sandbox the commits would be fetched from.
		if runErr == nil && inspectErr != nil {
			return ending{}, inspectErr
		}
	}
	if runErr != nil {
		return ending{}, runErr
	}
	if result.Status == "blocked" {
		// Another Iteration is unlikely to help, so none is spent on it.
		return a.stopAtPerson("the Implementer is blocked: " + result.Reason)
	}
	if a.pr != 0 && result.PR != a.pr {
		return ending{}, handBack{fmt.Sprintf("your result names pull request #%d, but this ticket's is #%d: put the work there and name it", result.PR, a.pr)}
	}
	pr, err := a.sh.readPullRequest(a.repo, result.PR)
	if err != nil {
		return ending{}, err
	}
	if err := a.checkPullRequest(pr, base); err != nil {
		return ending{}, err
	}
	if pr.Number == a.pr {
		a.report.emit(Event{Kind: EventPullRequestUpdated, PullRequest: pr.Number})
	} else {
		a.report.emit(Event{Kind: EventPullRequestOpened, PullRequest: pr.Number})
	}
	a.pr = pr.Number
	pr, ci, err := a.waitForCI(pr, base)
	if err != nil {
		return ending{}, err
	}
	switch ci {
	case ciPending:
		// Another pass can't unstick a pipeline, so no Iteration is spent on it.
		return a.stopAtPerson(fmt.Sprintf("CI not finished after %v", ciTimeout))
	case ciAbsent:
		a.report.setPhase(PhaseReviewing)
		a.report.emit(Event{Kind: EventCIAbsent, PullRequest: pr.Number})
	default:
		a.report.setPhase(PhaseReviewing)
		a.report.emit(Event{Kind: EventCIGreen, PullRequest: pr.Number})
	}
	if err := a.sh.stop(a.implementer); err != nil {
		return ending{}, err
	}

	// Everything from here on is pinned to this commit: it is what CI passed on,
	// what the Reviewer judges, and the only head the merge will accept.
	sha := pr.HeadRefOid
	v, err := a.runReviewer(iteration, pr, sha)
	if err != nil {
		return ending{}, err
	}
	a.report.emit(Event{Kind: EventVerdict, PullRequest: pr.Number, Verdict: &v, Reason: v.Reason})
	if err := a.sh.postVerdict(a.repo, pr.Number, v, sha); err != nil {
		return ending{}, err
	}

	switch mergeGate(ci, v) {
	case gateMerge:
		return ending{pr: pr.Number}, a.merge(pr, sha)
	case gateReadyForHuman:
		return a.stopAtPerson(v.Reason)
	}
	return ending{}, handBack{"the Reviewer requested changes: " + v.Reason}
}

// merge treats only a conflict as the Implementer's to fix: the base can move
// during a review, which no earlier check could have seen. GitHub may need a
// moment to work that out after the base moved.
func (a *attempt) merge(pr pullRequest, sha string) error {
	err := a.sh.merge(a.repo, pr.Number, sha, a.info.ViewerDefaultMergeMethod, a.values["GITHUB_TOKEN"])
	if err == nil {
		return nil
	}
	deadline := a.env.Clock.Now().Add(mergeableTimeout)
	for {
		now, readErr := a.sh.readPullRequest(a.repo, pr.Number)
		if errors.Is(readErr, errInterrupted) {
			return readErr
		}
		if readErr != nil {
			return err
		}
		if now.conflicted() {
			return mergeConflict(now)
		}
		if now.Mergeable != "UNKNOWN" || !a.env.Clock.Now().Before(deadline) {
			return err
		}
		a.env.Clock.Sleep(mergeablePollInterval)
	}
}

func mergeConflict(pr pullRequest) error {
	return handBack{fmt.Sprintf("pull request #%d has a merge conflict with %s: merge %s into its branch, resolve the conflict and push",
		pr.Number, pr.BaseRefName, pr.BaseRefName)}
}

// stopAtPerson moves the ticket, and its pull request if it has one, to
// ready-for-human.
func (a *attempt) stopAtPerson(reason string) (ending, error) {
	end := ending{pr: a.pr, readyForHuman: true, reason: reason}
	return end, a.sh.moveToReadyForHuman(a.repo, a.number, a.pr)
}

// checkPullRequest doesn't take the Implementer's word for it: merging must
// close this ticket, which is what unblocks its dependents. GitHub only honours
// a closing keyword on a merge into the default branch. All of it is for the
// Implementer to fix, so a failure is a hand-back.
func (a *attempt) checkPullRequest(pr pullRequest, defaultBranch string) error {
	if pr.State != "OPEN" {
		if pr.Number == a.pr {
			a.pr = 0 // the next pass can't push to it, and a person needn't look at it
			a.report.clearPullRequest()
		}
		return handBack{fmt.Sprintf("pull request #%d is %s", pr.Number, pr.State)}
	}
	if pr.BaseRefName != defaultBranch {
		return handBack{fmt.Sprintf("pull request #%d targets %s, not %s", pr.Number, pr.BaseRefName, defaultBranch)}
	}
	if !closesTicket(a.number).MatchString(pr.Body) {
		return handBack{fmt.Sprintf("pull request #%d does not contain \"Closes #%d\"", pr.Number, a.number)}
	}
	return nil
}

// waitForCI returns once CI is green or absent, or still pending at the
// timeout. Red CI and a conflict are the Implementer's to fix, so they come back
// as a hand-back. Checks that appear during the no-CI wait are waited for, and if
// CI is configured but no checks appear, the timeout applies. With an error, the
// state means nothing.
func (a *attempt) waitForCI(pr pullRequest, defaultBranch string) (pullRequest, ciState, error) {
	start := a.env.Clock.Now()
	deadline := start.Add(ciTimeout)
	waiting := false
	askedRepository := false // the repository's half of the no-CI rule, asked at most once
	for {
		// First, because CI may never run on a pull request that conflicts.
		if pr.conflicted() {
			return pr, ciPending, mergeConflict(pr)
		}
		switch pr.ci() {
		case ciGreen:
			return pr, ciGreen, nil
		case ciRed:
			return pr, ciRed, handBack{fmt.Sprintf("CI failed on pull request #%d: %s: look at those checks' output with gh, fix the cause and push",
				pr.Number, strings.Join(pr.failedChecks(), ", "))}
		}
		now := a.env.Clock.Now()
		if len(pr.Checks) == 0 && !askedRepository && !now.Before(start.Add(noCIWait)) {
			askedRepository = true
			configured, err := a.sh.ciConfigured(a.repo, defaultBranch)
			if err != nil {
				return pr, ciPending, err
			}
			if !configured {
				// Checks can appear while the repository is asked, so only a
				// pull request read after that still counts.
				if pr, err = a.readOpenPullRequest(pr.Number, defaultBranch); err != nil {
					return pr, ciPending, err
				}
				if len(pr.Checks) == 0 {
					return pr, ciAbsent, nil
				}
				continue
			}
		}
		if !now.Before(deadline) {
			return pr, ciPending, nil
		}
		if !waiting {
			a.report.setPhase(PhaseWaitingOnCI)
			a.report.emit(Event{Kind: EventWaitingForCI, PullRequest: pr.Number})
			waiting = true
		}
		a.env.Clock.Sleep(ciPollInterval)

		var err error
		if pr, err = a.readOpenPullRequest(pr.Number, defaultBranch); err != nil {
			return pr, ciPending, err
		}
	}
}

// readOpenPullRequest reads the pull request again, handing back if it no
// longer checks out.
func (a *attempt) readOpenPullRequest(number int, defaultBranch string) (pullRequest, error) {
	pr, err := a.sh.readPullRequest(a.repo, number)
	if err != nil {
		return pr, err
	}
	return pr, a.checkPullRequest(pr, defaultBranch)
}

// startSandbox creates a sandbox from the host checkout once moveCheckout has
// put it where the sandbox should start.
func (a *attempt) startSandbox(role, memory string, iteration int, grants []grant, repoSecrets []repoSecret, moveCheckout func() error) (*sandbox, error) {
	name := sandboxName(a.repo, a.number, role, iteration, a.env.Clock.Now())
	if err := a.createFromCheckout(name, memory, moveCheckout); err != nil {
		return nil, err
	}
	created := &sandbox{name: name}
	a.sandboxes = append(a.sandboxes, created)
	if err := a.driver.deliver(created, grants, repoSecrets, a.values); err != nil {
		return nil, err
	}
	kind := EventReviewerStarted
	if role == "implementer" {
		kind = EventImplementerStarted
	}
	a.report.emit(Event{Kind: kind, Sandbox: name})
	return created, nil
}

func (a *attempt) createFromCheckout(name, memory string, moveCheckout func() error) error {
	a.hostCheckout.Lock()
	defer a.hostCheckout.Unlock()
	if err := moveCheckout(); err != nil {
		return err
	}
	return a.driver.create(name, memory)
}

// changedWorkflowFiles judges the Implementer's pass from the commits it left
// in its sandbox. It takes the host checkout's lock because every Ticket
// Attempt of the run shares that checkout.
func (a *attempt) changedWorkflowFiles(base string) ([]string, error) {
	if a.implementer == nil {
		return nil, nil
	}
	a.hostCheckout.Lock()
	defer a.hostCheckout.Unlock()
	return a.checkout.changedWorkflowFiles(a.implementer.name, base)
}

// workflowChangeReason is what a person is told: which files need the change,
// and why it can't be made by an agent.
func workflowChangeReason(files []string) string {
	return fmt.Sprintf("the Implementer changed %s, so a person must make the workflow change: "+
		"no token lights-off uses has the workflow scope (ADR-0003)", strings.Join(files, ", "))
}

// runImplementer creates the Implementer's sandbox on the first pass and goes
// back to it after that, so that the work and its branch carry over.
func (a *attempt) runImplementer(iteration int) (implementerResult, error) {
	a.report.setPhase(PhaseImplementing)
	a.report.setIteration(iteration)
	prompt := implementerPrompt(a.repo, a.ticket)
	if a.implementer == nil {
		toBase := func() error { return a.checkout.moveToBranch(a.info.DefaultBranchRef.Name) }
		created, err := a.startSandbox("implementer", a.config.implementerLimit(), iteration, implementerGrants, a.config.secrets, toBase)
		if err != nil {
			return implementerResult{}, err
		}
		a.implementer = created
	} else {
		a.report.setPhase(PhaseImplementing)
		a.report.emit(Event{Kind: EventImplementerResumed, Sandbox: a.implementer.name})
		prompt = handBackPrompt(a.repo, a.ticket, a.pr, a.handedBack)
	}
	answer, err := a.driver.runAgent(a.implementer, "Implementer", prompt, a.agentLog("implementer", iteration))
	if err != nil {
		return implementerResult{}, err
	}
	result, err := parseImplementerResult(answer)
	if err != nil {
		return result, crashed("Implementer", err.Error())
	}
	return result, nil
}

// agentLog is where one agent pass's full output goes.
func (a *attempt) agentLog(role string, iteration int) string {
	return filepath.Join(a.logDir, fmt.Sprintf("%d-%s-%d.log", a.number, role, iteration))
}

// runReviewer always uses a new sandbox, so that its judgement stays independent
// of earlier reviews.
func (a *attempt) runReviewer(iteration int, pr pullRequest, sha string) (Verdict, error) {
	a.report.setPhase(PhaseReviewing)
	a.report.setIteration(iteration)
	var fixedPoint string
	toPullRequest := func() (err error) {
		fixedPoint, err = a.checkout.moveToPullRequest(pr, sha)
		return err
	}
	created, err := a.startSandbox("reviewer", reviewerMemory, iteration, reviewerGrants, nil, toPullRequest)
	if err != nil {
		return Verdict{}, err
	}
	answer, err := a.driver.runAgent(created, "Reviewer", reviewerPrompt(a.repo, a.ticket, pr, fixedPoint), a.agentLog("reviewer", iteration))
	var back handBack
	if err != nil && !errors.As(err, &back) {
		return Verdict{}, err
	}
	// The Implementer is about to need the memory if this goes back to it.
	if err := a.sh.stop(created); err != nil {
		return Verdict{}, err
	}
	if err != nil {
		return Verdict{}, err
	}
	v, err := parseVerdict(answer)
	if err != nil {
		return v, crashed("Reviewer", err.Error())
	}
	return v, nil
}

// removeSandboxes reports each failure, and the sandbox it leaves.
func (a *attempt) removeSandboxes() error {
	a.hostCheckout.Lock()
	defer a.hostCheckout.Unlock()
	var failed error
	for _, sb := range a.sandboxes {
		if err := a.cleanup.remove(sb); err != nil {
			a.report.emit(Event{Kind: EventSandboxRemoveFailed, Sandbox: sb.name, Reason: err.Error()})
			failed = errors.Join(failed, fmt.Errorf("%s: %w", sb.name, err))
		}
	}
	if failed == nil {
		a.report.emit(Event{Kind: EventSandboxesRemoved})
	}
	return failed
}
