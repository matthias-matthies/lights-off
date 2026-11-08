# Issue tracker: GitHub Issues

Tickets for this repo live as GitHub Issues in `matthias-matthies/lights-off`. Use the `gh` CLI for all operations.

Specs stay as markdown in the repo at `.scratch/<feature-slug>/spec.md`.

## Conventions

- One issue per ticket
- Triage state is recorded as a label (see `triage-labels.md` for the role strings)
- Blocking edges use GitHub's native issue dependencies, read and written through the `issues/<number>/dependencies/blocked_by` REST endpoint
- Conversation history goes in issue comments

## When a skill says "publish to the issue tracker"

Create an issue with `gh issue create`. Publish specs to `.scratch/<feature-slug>/spec.md` instead.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> --comments`. The user will normally pass the issue number directly.

## Wayfinding operations

Used by `/wayfinder`. The **map** is an issue with one **child** sub-issue per ticket.

- **Map**: an issue holding the Notes / Decisions-so-far / Fog body.
- **Child ticket**: a sub-issue of the map, with the question in the body. A `type:<research|prototype|grilling|task>` label records the ticket type.
- **Blocking**: GitHub's native issue dependencies. A ticket is unblocked when every issue blocking it is closed.
- **Frontier**: open, unblocked, unassigned sub-issues of the map; lowest number first.
- **Claim**: assign the issue before any work.
- **Resolve**: comment with the answer, close the issue, then append a context pointer (gist + link) to the map's Decisions-so-far.
