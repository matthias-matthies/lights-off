# Lights-off

A lightweight command-line tool for planning work with a human in a sandbox, then having sandboxed agents complete the resulting tickets unattended.

## Language

### Modes of work

**Planning Session**:
An interactive, human-led Claude Code session in a sandbox that produces a spec and tickets.
_Avoid_: Planning sandbox, grilling session

**Autonomous Run**:
One unattended pass over a selected set of tickets in a repository.
_Avoid_: Factory mode, batch, drain

**Planning Branch**:
The branch holding a Planning Session's documents, merged before any of its tickets are implemented.
_Avoid_: Spec branch

**Ticket Attempt**:
All the Iterations spent on one ticket, up to three.
_Avoid_: Job, task, run

**Iteration**:
One Implementer pass followed by the outcome that decides what happens next.
_Avoid_: Retry, round

**Hand-back**:
The end of an Iteration with a problem the Implementer can fix, passed to its next pass as the reason.
_Avoid_: Retry, rejection

**Tool failure**:
Lights-off itself could not continue, as opposed to an agent or a pull request falling short. It stops the Autonomous Run with a non-zero exit code and uses no Iteration.
_Avoid_: Crash, error

### Ticket Attempt phases

**Queued**:
A selected ticket waiting for a free slot or an open blocker. It has no status line.
_Avoid_: Pending, scheduled

**Implementing**:
The Implementer's pass is working the ticket.
_Avoid_: In progress, coding

**Waiting on CI**:
CI is running on the Ticket Attempt's pull request.
_Avoid_: Checking

**Reviewing**:
A fresh Reviewer is judging the pull request.
_Avoid_: In review

**Merged**:
Lights-off merged the pull request, ending the Ticket Attempt.
_Avoid_: Done

**Ready for human**:
The Ticket Attempt stopped at an open pull request for a person to decide.
_Avoid_: Needs review

**Interrupted**:
Ctrl+C ended the Ticket Attempt.
_Avoid_: Cancelled, stopped

### Roles

**Target Repository**:
A GitHub repository whose tickets lights-off plans and completes.
_Avoid_: Project, workspace

**Implementer**:
The agent that works a ticket through to an open pull request.
_Avoid_: Worker, coder

**Reviewer**:
A fresh agent, separate from the Implementer, that judges a pull request and returns a Verdict.
_Avoid_: Orchestrator, final gate

### Merging

**Merge Gate**:
The full set of conditions a pull request must pass before lights-off merges it: green CI and a Verdict of mergeable, low risk and Two-way Update.
_Avoid_: Checks, final gate

**Verdict**:
The Reviewer's structured judgement of a pull request: whether it is mergeable, its risk, and whether it is a Two-way or One-way Update.
_Avoid_: Approval, review result

**Risk**:
How much harm a mistake in a change could do before it is reverted, judged from what the change touches and how that is used, not from how easily it reverts. A change can be a Two-way Update and still be high risk. Low means a mistake would be minor and contained. Medium means it would break something people rely on, and anything affecting sign-in, permissions, payments or production data is at least medium. High means it could cause a wide outage, expose data or do lasting damage.
_Avoid_: Danger, severity

**Two-way Update**:
A change whose effects are fully undone by reverting its merge.
_Avoid_: Two-way door, reversible change

**One-way Update**:
A change whose effects a revert of its merge would not fully undo, such as a destructive data migration or a published release.
_Avoid_: One-way door, irreversible change
