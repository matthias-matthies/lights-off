package lightsoff

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// blocker is one issue a ticket is blocked by. It may come from GitHub's native
// links, from a "Blocked by" section in the ticket's text, or from both.
type blocker struct {
	ref  issueRef
	open bool
}

// assessment is what eligibility makes of a ticket: whether it may start, and if
// not, every check it failed, for the --ticket prompt to list. blockers holds
// every blocker it could resolve, whether open or closed, for the Dashboard's
// graph.
type assessment struct {
	ticket   ticket
	closed   bool
	failed   []string
	waiting  []string // failures that are a wait, such as open blockers
	blockers []blocker
}

func (a assessment) mayStart() bool { return !a.closed && len(a.failed) == 0 }

// assess decides whether a ticket may start. Failing to read the ticket or its
// label history is an error, so a gh problem can't pass for a check.
func (s shell) assess(repo string, number int, allowlist []string) (assessment, error) {
	t, err := s.readTicket(repo, number)
	if err != nil {
		return assessment{}, err
	}
	return s.assessTicket(repo, t, allowlist)
}

// assessTicket applies the same eligibility checks to a ticket that has already
// been read, so that the graph can list every open ticket without reading each
// one twice.
func (s shell) assessTicket(repo string, t ticket, allowlist []string) (assessment, error) {
	a := assessment{ticket: t, closed: t.State != "OPEN"}
	if a.closed {
		return a, nil
	}

	failed, err := s.labelCheck(repo, t, allowlist)
	if err != nil {
		return a, err
	}
	a.failed = append(a.failed, failed...)
	if _, claimed := t.Labels.find(agentInProgress); claimed {
		a.failed = append(a.failed, "it is already claimed: it carries "+agentInProgress)
	}
	failed, waiting, blockers := s.blockerCheck(repo, t)
	a.failed = append(a.failed, failed...)
	a.waiting = waiting
	a.blockers = blockers
	return a, nil
}

// labelCheck passes only if ready-for-agent is on the ticket and the last person
// to apply it is on the allowlist, because anyone who can label could otherwise
// direct an unattended agent.
func (s shell) labelCheck(repo string, t ticket, allowlist []string) ([]string, error) {
	if _, carried := t.Labels.find(readyForAgent); !carried {
		return []string{"it has no " + readyForAgent + " label"}, nil
	}
	appliedBy, err := s.lastLabelledBy(repo, t.Number, readyForAgent)
	if err != nil {
		return nil, err
	}
	switch {
	case appliedBy == "":
		return []string{readyForAgent + " has no record of who applied it"}, nil
	case !slices.ContainsFunc(allowlist, func(user string) bool { return strings.EqualFold(user, appliedBy) }):
		return []string{fmt.Sprintf("%s was applied by %s, who is not on the allowlist", readyForAgent, appliedBy)}, nil
	}
	return nil, nil
}

// issueRef is one issue, in the form GitHub would link it from this repository.
type issueRef struct {
	repo   string
	number int
}

func (r issueRef) from(repo string) string {
	if r.repo == repo {
		return fmt.Sprintf("#%d", r.number)
	}
	return fmt.Sprintf("%s#%d", r.repo, r.number)
}

// blockerCheck reads blockers from GitHub's native links and from "Blocked by"
// text, because to-tickets writes whichever the tracker supports. A blocker it
// can't resolve fails the check too, because lights-off never assumes a ticket
// is free to start.
func (s shell) blockerCheck(repo string, t ticket) (failed, waiting []string, blockers []blocker) {

	unresolved := func(detail string) {
		failed = append(failed, "a blocker can't be resolved, so it isn't treated as free to start: "+detail)
	}

	open := map[issueRef]bool{} // every blocker, and whether it is open
	var order []issueRef
	add := func(ref issueRef, isOpen bool) {
		if _, seen := open[ref]; !seen {
			order = append(order, ref)
		}
		open[ref] = open[ref] || isOpen
	}

	native, err := s.nativeBlockers(repo, t.Number)
	if err != nil {
		unresolved("native blocking links: " + err.Error())
	}
	for ref, isOpen := range native {
		add(ref, isOpen)
	}
	slices.SortFunc(order, compareRefs)

	refs, notRefs := blockersInText(repo, t.Body)
	for _, text := range notRefs {
		unresolved(fmt.Sprintf("%q is not a ticket reference", text))
	}
	for _, ref := range refs {
		if _, known := open[ref]; known {
			continue
		}
		state, err := s.issueState(ref)
		if err != nil {
			unresolved(ref.from(repo) + ": " + err.Error())
			continue
		}
		add(ref, isOpen(state))
	}

	var openRefs []string
	for _, ref := range order {
		if open[ref] {
			openRefs = append(openRefs, ref.from(repo))
		}
	}
	if len(openRefs) > 0 {
		held := "it has open blockers: " + strings.Join(openRefs, ", ")
		failed = append([]string{held}, failed...)
		waiting = append(waiting, held)
	}
	for _, ref := range order {
		blockers = append(blockers, blocker{ref: ref, open: open[ref]})
	}
	return failed, waiting, blockers
}

func compareRefs(a, b issueRef) int {
	return cmp.Or(strings.Compare(a.repo, b.repo), cmp.Compare(a.number, b.number))
}

var (
	// blockedByLine is a "Blocked by" heading, or a line that starts with it,
	// in plain or bold text. The last group is anything after it on the line.
	blockedByLine = regexp.MustCompile(`(?i)^\s*(#{1,6}\s*)?(?:\*\*|__)?blocked by(?:\*\*|__)?\s*:?\s*(?:\*\*|__)?\s*(.*)$`)
	headingLine   = regexp.MustCompile(`^\s*#{1,6}\s`)
	listMarker    = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s*`)
	noneLine      = regexp.MustCompile(`(?i)^(?:\*\*|__|_|\*)?(?:none|nothing|n/a)\b|^[-–—]+$`)
	issueLink     = regexp.MustCompile(`https://github\.com/([A-Za-z0-9][\w.-]*/[\w.-]+)/(?:issues|pull)/(\d+)\b`)
	anyLink       = regexp.MustCompile(`https?://\S+`)
	issueMention  = regexp.MustCompile(`(?:\b([A-Za-z0-9][\w.-]*/[\w.-]+))?#(\d+)\b`)
)

// blockersInText returns the issues "Blocked by" sections and lines refer to,
// and any line in them that names something without referring to an issue.
func blockersInText(repo, body string) (refs []issueRef, notRefs []string) {
	var listed []string // lines that should each name blockers
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		match := blockedByLine.FindStringSubmatch(lines[i])
		if match == nil {
			continue
		}
		listed = append(listed, match[2])
		heading := match[1] != ""
		if !heading && strings.TrimSpace(match[2]) != "" {
			continue // a single line
		}
		// A section runs to the next heading. A bare line's list runs only to
		// the first blank line after it, because the template's other fields follow.
		started := false
		for i+1 < len(lines) && !headingLine.MatchString(lines[i+1]) && !blockedByLine.MatchString(lines[i+1]) {
			blank := strings.TrimSpace(lines[i+1]) == ""
			if !heading && blank && started {
				break
			}
			started = started || !blank
			i++
			listed = append(listed, lines[i])
		}
	}

	for _, line := range listed {
		line = strings.TrimSpace(listMarker.ReplaceAllString(line, ""))
		if line == "" || noneLine.MatchString(line) {
			continue
		}
		found := false
		for _, m := range issueLink.FindAllStringSubmatch(line, -1) {
			refs = append(refs, issueRef{repo: m[1], number: atoi(m[2])})
			found = true
		}
		// Other links can carry a fragment that looks like a mention.
		for _, m := range issueMention.FindAllStringSubmatch(anyLink.ReplaceAllString(line, ""), -1) {
			refs = append(refs, issueRef{repo: cmp.Or(m[1], repo), number: atoi(m[2])})
			found = true
		}
		if !found {
			notRefs = append(notRefs, line)
		}
	}
	return refs, notRefs
}

// atoi is only given what the patterns above matched as digits.
func atoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}
