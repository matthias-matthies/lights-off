package lightsoff

import (
	"errors"
	"fmt"
	"strings"
)

// runSeveral is an Autonomous Run over up to opts.n eligible tickets, as many at
// once as host memory allows. It never asks anything, so it needs no terminal.
//
// Tickets are listed and assessed again whenever a slot is free, and again each
// time a ticket ends, so a ticket a merge earlier in the run unblocked can start
// in it, and one blocked by a ticket still running or failed waits or is skipped.
func runSeveral(opts options, config repoConfig, values secrets, run autonomousRun, env Env) int {
	atOnce, err := ticketsAtOnce(opts.n, config, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
		return exitFailure
	}
	report := &reporter{out: env.Stdout, onEvent: env.OnEvent}
	s := scheduler{repo: opts.repo, allowlist: config.allowlist, sh: shell{exec: stoppable(env.Exec, run.stop)}, report: report,
		attempted: map[int]bool{}, queued: map[int]string{}, skipped: map[int]string{}}
	ended := make(chan outcome)
	var outcomes []outcome
	code := exitOK
	started, running := 0, 0
	for {
		// A Tool failure or Ctrl+C starts nothing more, and the tickets already
		// running are left to end, which after Ctrl+C they do at once.
		for code == exitOK && !closed(run.stop) && running < atOnce && started < opts.n {
			t, found, err := s.next()
			if errors.Is(err, errInterrupted) {
				break
			}
			if err != nil {
				fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
				code = exitFailure
				break
			}
			if !found {
				break
			}
			// A ticket counts toward --n however it ends, so that --n limits
			// what a bad run can spend.
			s.attempted[t.Number] = true
			started++
			a := newAttempt(opts.repo, t, config, values, run, env, report)
			if o, claimed := a.claim(); !claimed {
				// With no ticket, Ctrl+C stopped the claim before it changed anything.
				if o.ticket != 0 {
					outcomes = append(outcomes, o)
					code = exitFailure
				}
				break
			}
			running++
			go func() { ended <- a.run() }()
		}
		if code == exitOK && !closed(run.stop) && running == atOnce && started < opts.n {
			// A ticket that is eligible but has no slot is waiting, not
			// skipped; the Dashboard can show it queued without a status line.
			if err := s.peek(); err != nil && !errors.Is(err, errInterrupted) {
				fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
				code = exitFailure
			}
		}
		if running == 0 {
			break
		}
		o := <-ended
		outcomes = append(outcomes, o)
		code = max(code, o.code) // a Tool failure anywhere fails the run
		running--
	}
	if code == exitOK && !closed(run.stop) && started < opts.n {
		// A small backlog is not a failure.
		fmt.Fprintf(env.Stdout, "lights-off: %d of %d tickets started: no other ticket is eligible\n", started, opts.n)
	}
	summary := printSummary(env.Stdout, outcomes, run.logDir)
	report.report(Event{Kind: EventSummary, Summary: summary})
	return code
}

// memoryShare is how much of host memory a run's sandboxes may take together.
const memoryShare = 0.75

// ticketsAtOnce is the lower of n and how many tickets fit in memoryShare of
// host memory at their peak, but at least one. A ticket's peak is its larger
// sandbox, because the Implementer's is stopped while the Reviewer's runs.
func ticketsAtOnce(n int, config repoConfig, env Env) (int, error) {
	host, err := env.HostMemory()
	if err != nil {
		return 0, fmt.Errorf("reading host memory: %w", err)
	}
	peak, peakBytes := "", uint64(0)
	for _, limit := range []string{config.implementerLimit(), reviewerMemory} {
		bytes, err := memoryBytes(limit)
		if err != nil {
			return 0, err
		}
		if bytes > peakBytes {
			peak, peakBytes = limit, bytes
		}
	}
	fit := int(float64(host) * memoryShare / float64(peakBytes))
	atOnce := max(1, min(n, fit))
	fmt.Fprintf(env.Stdout, "lights-off: up to %d %s at once: %.0f%% of %.1f GB host memory at %s each\n",
		atOnce, plural(atOnce, "ticket", "tickets"), memoryShare*100, float64(host)/(1<<30), peak)
	return atOnce, nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// scheduler picks the tickets of one Autonomous Run.
type scheduler struct {
	repo      string
	allowlist []string
	sh        shell
	report    *reporter

	attempted map[int]bool   // tickets already started in this run
	queued    map[int]string // why each ticket was last reported queued
	skipped   map[int]string // why each ticket was last reported skipped
}

// next returns the oldest ticket that may start now, if there is one. A skipped
// ticket is reported once for each new reason, because it is looked at again
// before every pick.
func (s *scheduler) next() (ticket, bool, error) {
	candidates, err := s.sh.listReadyTickets(s.repo)
	if err != nil {
		return ticket{}, false, err
	}
	for _, number := range candidates {
		if s.attempted[number] {
			continue
		}
		a, err := s.sh.assess(s.repo, number, s.allowlist)
		if err != nil {
			return ticket{}, false, err
		}
		if a.mayStart() {
			return a.ticket, true, nil
		}
		s.skip(a)
	}
	return ticket{}, false, nil
}

// peek reports the ticket that would start next if a slot were free. It starts
// nothing, but tells the Dashboard that the ticket is queued.
func (s *scheduler) peek() error {
	candidates, err := s.sh.listReadyTickets(s.repo)
	if err != nil {
		return err
	}
	for _, number := range candidates {
		if s.attempted[number] {
			continue
		}
		a, err := s.sh.assess(s.repo, number, s.allowlist)
		if err != nil {
			return err
		}
		if a.mayStart() {
			s.queue(a.ticket.Number, "waiting for a free slot")
			return nil
		}
		s.skip(a)
	}
	return nil
}

// skip reports a ticket that may not start. One held back by an open blocker is
// also waiting, so it is queued as well as skipped.
func (s *scheduler) skip(a assessment) {
	why := strings.Join(a.failed, "; ")
	if a.closed {
		why = "it is closed"
	}
	if len(a.waiting) > 0 {
		s.queue(a.ticket.Number, strings.Join(a.waiting, "; "))
	}
	if s.skipped[a.ticket.Number] != why {
		s.skipped[a.ticket.Number] = why
		s.forTicket(a.ticket.Number).emit(Event{Kind: EventSkipped, Reason: why})
	}
}

func (s *scheduler) queue(number int, reason string) {
	if s.queued[number] == reason {
		return
	}
	s.queued[number] = reason
	s.forTicket(number).emit(Event{Kind: EventQueued, Reason: reason})
}

func (s *scheduler) forTicket(number int) *attemptReporter {
	return &attemptReporter{out: s.report, ticket: number, phase: PhaseQueued}
}
