package lightsoff

import (
	"errors"
	"fmt"
)

// errInterrupted ends whatever a Ticket Attempt was doing when a person pressed
// Ctrl+C. It is neither a Hand-back nor a Tool failure.
var errInterrupted = errors.New("interrupted by Ctrl+C")

// closed reports whether ch has closed; a nil ch never does.
func closed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return ch != nil
	default:
		return false
	}
}

// stoppable refuses every command once stop has closed, so that a Ticket
// Attempt goes no further than the step it was on, and reports a command stop
// killed as interrupted. Cleanup after Ctrl+C uses the unguarded exec.
func stoppable(exec func(Command) (Result, error), stop <-chan struct{}) func(Command) (Result, error) {
	return func(c Command) (Result, error) {
		if closed(stop) {
			return Result{}, errInterrupted
		}
		res, err := exec(c)
		if err == nil && res.Interrupted {
			return res, errInterrupted
		}
		return res, err
	}
}

// announceInterrupt says at once that Ctrl+C was heard, because stopping the
// agents can take a moment. It ends when done closes.
func announceInterrupt(stop <-chan struct{}, done <-chan struct{}, env Env) {
	select {
	case <-stop:
		fmt.Fprintln(env.Stdout, "lights-off: Ctrl+C: stopping the agents and keeping their sandboxes")
	case <-done:
	}
}

// interrupted ends an attempt a person stopped: every sandbox is stopped and
// kept, and a ticket with no pull request yet goes back to ready-for-agent so
// that it can be picked up again. One with a pull request keeps
// agent-in-progress, because work for it is out on GitHub. A person stopping a
// run is no Tool failure, so it exits 0.
//
// GitHub is asked as well, because an Implementer stopped mid-pass may have
// opened a pull request lights-off hasn't read yet, and one handed back for not
// checking out is still open.
func (a *attempt) interrupted() outcome {
	a.report.setPhase(PhaseInterrupted)
	var failed error
	for _, sb := range a.sandboxes {
		if err := a.cleanup.stop(sb); err != nil {
			failed = errors.Join(failed, err)
		}
	}
	pr := a.pr
	if pr == 0 {
		found, err := a.cleanup.openPullRequestClosing(a.repo, a.number)
		if err != nil {
			failed = errors.Join(failed, err)
		}
		pr = found
	}
	var o outcome
	switch {
	case failed != nil:
	case pr == 0:
		if err := a.cleanup.relabel(a.repo, a.number, readyForAgent, agentInProgress); err != nil {
			failed = errors.Join(failed, err)
		} else {
			a.report.emit(Event{Kind: EventInterrupted, Reason: errInterrupted.Error()})
			o = a.keep(exitOK, "back to "+readyForAgent)
		}
	default:
		a.report.emit(Event{Kind: EventInterrupted, PullRequest: pr, Reason: errInterrupted.Error()})
		o = a.keep(exitOK, fmt.Sprintf("pull request #%d open, %s kept", pr, agentInProgress))
	}
	if failed != nil {
		a.report.emit(Event{Kind: EventStopped, Reason: "after Ctrl+C: " + failed.Error()})
		return a.keep(exitFailure, fmt.Sprintf("stopped after Ctrl+C: %v", failed))
	}
	o.interrupted = true
	return o
}
