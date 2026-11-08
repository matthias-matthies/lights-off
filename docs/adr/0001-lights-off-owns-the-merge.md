# lights-off owns the merge, and GitHub enforces only CI

The Reviewer returns a Verdict and lights-off performs the merge with plain code, because a merge made from inside a sandbox has already happened by the time lights-off could check it. A `dsh` orchestrator was rejected because it adds model cost and no check beyond the Reviewer, and letting the Implementer merge would let it judge its own work. GitHub enforces only CI, and each Target Repository is responsible for setting CI up, so a repository with no CI counts as green and its merges rest on the Reviewer's Verdict alone.

## Consequences

The Merge Gate is not a security boundary. The Implementer's push access uses the same GitHub permission as merging, so it could technically merge its own pull request; this is accepted as the cost of staying lightweight.
