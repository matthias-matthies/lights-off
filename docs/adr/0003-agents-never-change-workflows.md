# Agents never change GitHub Actions workflows

No GitHub token lights-off hands an agent or uses itself has the `workflow` scope, or the Workflows permission for a fine-grained token, so GitHub refuses any push that creates or changes a file under `.github/workflows/`. On a `pull_request` event from a branch in the same repository, GitHub runs the workflows from that branch, so a workflow pushed by an agent that a ticket or a file had prompt-injected would run in Actions straight away, with the repository's secrets and whatever its workflow grants `GITHUB_TOKEN`, before the Reviewer or the Merge Gate sees it. Neither of those can stop that, so the token is the only control. Workflow changes are made by a person.

## Consequences

A ticket that needs a workflow change can't be completed by an Autonomous Run and belongs with a person. Merging a pull request that changes a workflow also needs the scope, so lights-off couldn't merge one either. Do not grant the scope to unstick such a ticket.

CI still runs the Implementer's code, because the tests it adds are code too, so a workflow that runs on `pull_request` must not be given secrets. One that needs them runs on pushes to the default branch only.
