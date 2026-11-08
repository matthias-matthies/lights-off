package lightsoff

import "fmt"

// ticketGraph is the Dashboard's view of a Target Repository's open tickets:
// every open issue, whether it is eligible, why not if it isn't, and the
// blockers that hold it back. The page draws it as a layered graph.
type ticketGraph struct {
	Repo    string        `json:"repo"`
	Tickets []graphTicket `json:"tickets"`
}

type graphTicket struct {
	Number       int            `json:"number"`
	Title        string         `json:"title"`
	Labels       []string       `json:"labels"`
	URL          string         `json:"url"`
	Blockers     []graphBlocker `json:"blockers"`
	Eligible     bool           `json:"eligible"`
	FailedChecks []string       `json:"failedChecks"`
}

// graphBlocker is one issue a ticket is blocked by, from either native links or
// the ticket's "Blocked by" text. Open is what the layered layout follows.
type graphBlocker struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	URL    string `json:"url"`
	Open   bool   `json:"open"`
}

// buildTicketGraph reads every open ticket and runs it through eligibility. It
// is called by the graph endpoint, never by a background poller, so lights-off
// makes no gh calls for the graph while no page asks for it.
func (s shell) buildTicketGraph(repo string, allowlist []string) (ticketGraph, error) {
	tickets, err := s.listOpenTickets(repo)
	if err != nil {
		return ticketGraph{}, err
	}
	g := ticketGraph{Repo: repo, Tickets: make([]graphTicket, 0, len(tickets))}
	for _, t := range tickets {
		a, err := s.assessTicket(repo, t, allowlist)
		if err != nil {
			return ticketGraph{}, err
		}
		gt := graphTicket{
			Number:       t.Number,
			Title:        t.Title,
			Labels:       make([]string, 0, len(t.Labels)),
			URL:          t.URL,
			Blockers:     make([]graphBlocker, 0, len(a.blockers)),
			Eligible:     a.mayStart(),
			FailedChecks: append([]string{}, a.failed...),
		}
		if gt.URL == "" {
			gt.URL = issueURL(repo, t.Number)
		}
		for _, label := range t.Labels {
			gt.Labels = append(gt.Labels, label.Name)
		}
		for _, b := range a.blockers {
			gt.Blockers = append(gt.Blockers, graphBlocker{
				Repo:   b.ref.repo,
				Number: b.ref.number,
				URL:    issueURL(b.ref.repo, b.ref.number),
				Open:   b.open,
			})
		}
		g.Tickets = append(g.Tickets, gt)
	}
	return g, nil
}

func issueURL(repo string, number int) string {
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, number)
}
