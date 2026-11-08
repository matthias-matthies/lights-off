package lightsoff

import (
	"fmt"
	"io"
	"strings"
)

// AttemptPhase is where a Ticket Attempt is. A skipped ticket has no phase.
type AttemptPhase string

const (
	PhaseQueued        AttemptPhase = "Queued"
	PhaseImplementing  AttemptPhase = "Implementing"
	PhaseWaitingOnCI   AttemptPhase = "Waiting on CI"
	PhaseReviewing     AttemptPhase = "Reviewing"
	PhaseMerged        AttemptPhase = "Merged"
	PhaseReadyForHuman AttemptPhase = "Ready for human"
	PhaseInterrupted   AttemptPhase = "Interrupted"
)

// EventKind is what a ticket event reports, which is what decides its status
// line.
type EventKind string

const (
	EventQueued              EventKind = "queued"
	EventSkipped             EventKind = "skipped"
	EventClaimed             EventKind = "claimed"
	EventImplementerStarted  EventKind = "implementer-started"
	EventImplementerResumed  EventKind = "implementer-resumed"
	EventPullRequestOpened   EventKind = "pull-request-opened"
	EventPullRequestUpdated  EventKind = "pull-request-updated"
	EventWaitingForCI        EventKind = "waiting-for-ci"
	EventCIGreen             EventKind = "ci-green"
	EventCIAbsent            EventKind = "ci-absent"
	EventReviewerStarted     EventKind = "reviewer-started"
	EventVerdict             EventKind = "verdict"
	EventHandedBack          EventKind = "handed-back"
	EventMerged              EventKind = "merged"
	EventReadyForHuman       EventKind = "ready-for-human"
	EventSandboxKept         EventKind = "sandbox-kept"
	EventSandboxesRemoved    EventKind = "sandboxes-removed"
	EventSandboxRemoveFailed EventKind = "sandbox-removal-failed"
	EventStillOpenAfterMerge EventKind = "still-open-after-merge"
	EventInterrupted         EventKind = "interrupted"
	EventStopped             EventKind = "stopped"
	EventSummary             EventKind = "summary"
	EventSessionEnded        EventKind = "session-ended"
)

// ReasonKind says what an Event's Reason explains. A Hand-back keeps its
// reason through the Iteration it starts, so the Dashboard needs this to label
// it as a Hand-back rather than as the current state's own reason.
type ReasonKind string

const (
	ReasonHandBack      ReasonKind = "hand-back"
	ReasonReadyForHuman ReasonKind = "ready-for-human"
	ReasonStopped       ReasonKind = "stopped"
)

// Event is one structured ticket event. The Reporter renders its status line
// and hands it to whatever is subscribed, such as the Dashboard.
//
// The JSON field names are the Dashboard's contract: each event is also an SSE
// message, and a subscriber that reconnects receives the latest event for every
// ticket as the same shape. Each ticket event is a complete snapshot of the
// Ticket Attempt's state, so a subscriber replaces an older event with the new
// one. A final summary or session-ended event belongs to no ticket and ends the
// stream.
type Event struct {
	Kind        EventKind    `json:"kind"`
	Ticket      int          `json:"ticket"`
	Phase       AttemptPhase `json:"phase,omitempty"` // empty for a skipped ticket
	Iteration   int          `json:"iteration,omitempty"`
	Verdict     *Verdict     `json:"verdict,omitempty"`
	PullRequest int          `json:"pullRequest,omitempty"`
	Sandbox     string       `json:"sandbox,omitempty"`
	Reason      string       `json:"reason,omitempty"`
	ReasonKind  ReasonKind   `json:"reasonKind,omitempty"`
	Summary     string       `json:"summary,omitempty"` // the final report of an Autonomous Run
}

// statusLine renders this event's status line. A queued event has none: it only
// tells the Dashboard that the ticket is waiting.
func (e Event) statusLine() string {
	switch e.Kind {
	case EventQueued:
		return ""
	case EventSkipped:
		return "skipped: " + e.Reason
	case EventClaimed:
		return "claimed"
	case EventImplementerStarted:
		return "implementer started in sandbox " + e.Sandbox
	case EventImplementerResumed:
		return "implementer resumed in sandbox " + e.Sandbox
	case EventPullRequestOpened:
		return fmt.Sprintf("pull request #%d opened", e.PullRequest)
	case EventPullRequestUpdated:
		return fmt.Sprintf("pull request #%d updated", e.PullRequest)
	case EventWaitingForCI:
		return "waiting for CI"
	case EventCIGreen:
		return "CI green"
	case EventCIAbsent:
		return "no CI: counted as green, so the Verdict alone decides"
	case EventReviewerStarted:
		return "reviewer started in sandbox " + e.Sandbox
	case EventVerdict:
		if e.Verdict == nil {
			return "verdict: unknown"
		}
		return "verdict: " + e.Verdict.describe()
	case EventHandedBack:
		// A Hand-back that starts the next Iteration carries that Iteration. The
		// last one has no next, so it carries the Iteration that ended and the
		// Ready for human phase.
		ended := e.Iteration
		if e.Phase == PhaseImplementing {
			ended--
		}
		return fmt.Sprintf("handed back after iteration %d of %d: %s", ended, maxIterations, e.Reason)
	case EventMerged:
		return fmt.Sprintf("merged pull request #%d", e.PullRequest)
	case EventReadyForHuman:
		if e.PullRequest == 0 {
			return "ready for human: no pull request: " + e.Reason
		}
		return fmt.Sprintf("ready for human: pull request #%d stays open: %s", e.PullRequest, e.Reason)
	case EventSandboxKept:
		return "sandbox kept: " + e.Sandbox
	case EventSandboxesRemoved:
		return "sandboxes removed"
	case EventSandboxRemoveFailed:
		return fmt.Sprintf("sandbox removal failed: %s: %s", e.Sandbox, e.Reason)
	case EventStillOpenAfterMerge:
		return fmt.Sprintf("still open %s after the merge: tickets it blocks wait until GitHub closes it", e.Reason)
	case EventInterrupted:
		if e.PullRequest == 0 {
			return "interrupted: back to " + readyForAgent
		}
		return fmt.Sprintf("interrupted: pull request #%d stays open and the ticket keeps %s", e.PullRequest, agentInProgress)
	case EventStopped:
		return "stopped: " + e.Reason
	}
	return ""
}

// reporter renders one status line per ticket event and hands the structured
// event to whatever subscribed to the run.
type reporter struct {
	out     io.Writer
	onEvent func(Event)
}

func (r *reporter) report(e Event) {
	line := strings.Join(strings.Fields(e.statusLine()), " ")
	if line != "" {
		fmt.Fprintf(r.out, "#%d %s\n", e.Ticket, line)
	}
	if r.onEvent != nil {
		r.onEvent(e)
	}
}

// attemptReporter fills every event with the Ticket Attempt's current state, so
// that a subscriber never has to work it out from free text, and so that the
// latest event of a ticket is enough to draw it without the earlier ones.
type attemptReporter struct {
	out         *reporter
	ticket      int
	phase       AttemptPhase
	iteration   int
	verdict     *Verdict
	pullRequest int
	sandbox     string
	reason      string     // why the Attempt waits, or what sent it into a later Iteration
	reasonKind  ReasonKind // what that reason explains
}

func (r *attemptReporter) setPhase(phase AttemptPhase) { r.phase = phase }
func (r *attemptReporter) setIteration(iteration int)  { r.iteration = iteration }

// clearPullRequest forgets a pull request the attempt no longer has, so that a
// later event doesn't keep naming one that was closed.
func (r *attemptReporter) clearPullRequest() { r.pullRequest = 0 }

func (r *attemptReporter) emit(e Event) {
	e.Ticket = r.ticket
	// A skipped ticket has no Attempt Phase, unlike everything else it reports.
	if e.Kind == EventSkipped {
		e.Phase = ""
	} else if e.Phase == "" {
		e.Phase = r.phase
	}
	if e.Iteration == 0 {
		e.Iteration = r.iteration
	}
	// A Hand-back into a later Iteration makes the previous Iteration's Verdict
	// stale. A final Hand-back keeps it, because no later Reviewer replaces it.
	if e.Kind == EventHandedBack && e.Phase == PhaseImplementing {
		r.verdict = nil
	}
	if e.Verdict == nil {
		e.Verdict = r.verdict
	} else {
		r.verdict = e.Verdict
	}
	if e.PullRequest == 0 {
		e.PullRequest = r.pullRequest
	} else {
		r.pullRequest = e.PullRequest
	}
	if e.Sandbox == "" {
		e.Sandbox = r.sandbox
	} else {
		r.sandbox = e.Sandbox
	}
	// A Hand-back explains the Iteration it starts, so it stays with the ticket
	// through that Iteration's events until a newer Verdict or outcome replaces
	// it. A ticket waiting for a person keeps the reason that put it there,
	// because the sandbox events after it describe the same waiting state.
	switch e.Kind {
	case EventHandedBack:
		r.reason, r.reasonKind = e.Reason, ReasonHandBack
		e.ReasonKind = ReasonHandBack
	case EventReadyForHuman:
		r.reason, r.reasonKind = e.Reason, ReasonReadyForHuman
		e.ReasonKind = ReasonReadyForHuman
	case EventStopped:
		r.reason, r.reasonKind = e.Reason, ReasonStopped
		e.ReasonKind = ReasonStopped
	case EventVerdict:
		// The new Verdict, and its own reason, replace the Hand-back that the
		// previous Iteration ended on. Its reason belongs to that event alone.
		r.reason, r.reasonKind = "", ""
	case EventMerged:
		r.reason, r.reasonKind = "", ""
	}
	if e.Reason == "" && e.Kind != EventVerdict {
		e.Reason = r.reason
		e.ReasonKind = r.reasonKind
	}
	r.out.report(e)
}
