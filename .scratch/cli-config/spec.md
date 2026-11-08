# Opting in Target Repositories and editing Allowlists from the command line

## Problem Statement

To opt a GitHub repository in to lights-off, or to change who is on its Allowlist, a person has to open `config.toml` by hand and write lines in the narrow TOML subset lights-off reads. It is easy to get the syntax wrong (a multi-line array, an escape, a repeated key), and the mistake only shows up the next time lights-off runs, as a refusal naming a line. Today the "not opted in" refusal prints raw TOML to paste in, which is awkward for a first run.

Opting a repository out by hand has a trap. Removing a Target Repository's table can leave a value in `secrets.env` that no remaining repository declares. Lights-off then refuses to start on *every* repository until the person finds and deletes that value.

## Solution

Lights-off gets two groups of config-only subcommands:

- `lights-off repo add <owner>/<repo> [--allow=<user>,...]` opts a repository in, optionally with its first Allowlist users.
- `lights-off repo remove <owner>/<repo>` opts a Target Repository out.
- `lights-off repo list` shows each Target Repository and its Allowlist.
- `lights-off allowlist add <owner>/<repo> <user>...` adds users to a Target Repository's Allowlist.
- `lights-off allowlist remove <owner>/<repo> <user>...` removes them.

These commands edit `config.toml` in place and keep the person's comments and layout. They never write a file lights-off can't read back. They work offline: no `gh`, `git` or `sbx`, and they don't read `secrets.env` values. The "not opted in" refusal now points to the `repo add` command. The README leads with the commands and keeps the TOML as a reference.

## User Stories

1. As a new user, I want to opt a repository in with one command, so that I don't have to learn the config file's syntax before my first run.
2. As a new user, I want the "not opted in" refusal to tell me the exact command to run, so that I can copy it and try again.
3. As a new user, I want to give the first Allowlist users when I opt in, so that one command makes the repository ready for `--n` runs.
4. As a new user with no config directory yet, I want `repo add` to create the directory and `config.toml` for me, so that I don't have to create them by hand first.
5. As a user, I want a newly created config directory and file to be private to me, so that other local users can't change which repositories agents work on.
6. As a user who changed my config file's mode, I want edits to keep that mode, so that a command doesn't quietly loosen or tighten it.
7. As a user, I want my comments and layout in `config.toml` kept when a command edits it, so that notes I wrote aren't lost.
8. As a user, I want a newly opted-in repository's table added at the end of the file, so that the rest of the file stays as I arranged it.
9. As a user, I want a command to refuse to edit a `config.toml` it can't read, naming the file and line, so that it never builds on a file it misunderstood.
10. As a user, I want edits to be atomic, so that a crash or a full disk can't leave `config.toml` half-written.
11. As a user, I want opting in a repository that's already opted in to succeed and say so, so that I can re-run a command safely.
12. As a user, I want `repo add --allow=…` on a repository that's already opted in to add any missing users to its Allowlist, so that the command from the refusal message is always safe to re-run.
13. As a user, I want opting in without any users to warn me that `--n` will pick nothing up until someone is on the Allowlist, so that I'm not surprised by an empty run.
14. As a user, I want the repository name checked against the `owner/repo` format lights-off already enforces, so that obvious typos are refused before anything is written.
15. As a user, I want these commands to work without network access or GitHub authentication, so that editing the config never depends on GitHub being reachable.
16. As a user, I want to opt a Target Repository out with one command, so that agents can no longer work on it.
17. As a user, I want opting out to remove the repository's secret declarations too, so that nothing about it is left in `config.toml`.
18. As a user, I want opting out to print the lines it removed, so that I can paste them back if I made a mistake.
19. As a user, I want opting out to name any secrets that no remaining Target Repository declares, and tell me to delete them from `secrets.env`, so that my next run on another repository isn't refused.
20. As a user, I want lights-off never to edit `secrets.env` itself, so that secret values are only ever handled by me.
21. As a user, I want opting out to leave the repository's cached checkout alone, so that the command only changes configuration and stays predictable.
22. As a user, I want opting out of a repository that isn't opted in to fail with a clear message, so that a typo doesn't look like a success.
23. As a user, I want opting out to proceed without a confirmation prompt, so that it works in scripts and the printed lines are enough to undo it.
24. As a user, I want to add one or more users to a Target Repository's Allowlist in a single command, so that I can onboard several people at once.
25. As a user, I want to remove one or more users from an Allowlist in a single command, so that I can offboard several people at once.
26. As a user, I want a multi-user command to be all-or-nothing, so that one bad name doesn't leave the Allowlist half-changed.
27. As a user, I want adding a user who's already on the Allowlist to succeed and say so, ignoring case, so that `Alice` and `alice` aren't listed twice.
28. As a user, I want removing a user who isn't on the Allowlist to fail with a clear message, so that a typo doesn't look like a success.
29. As a user, I want removal to match usernames ignoring case, as eligibility does, so that what I remove is what lights-off would have trusted.
30. As a user, I want usernames stored as I typed them, so that the file reads the way I wrote it.
31. As a user, I want removing the last user to warn me that `--n` will now pick nothing up, so that I understand the consequence.
32. As a user, I want usernames checked against GitHub's user-login format, so that typos and malformed names are refused.
33. As a security-minded user, I want bot logins such as `dependabot[bot]` refused by the CLI, so that I don't accidentally let anything that can drive a bot start an agent.
34. As a user who really wants an unusual entry, I want editing the file by hand to still work, so that the CLI's stricter checks don't lock me out.
35. As a user, I want Allowlist edits to keep a trailing comment on the `allowlist` line, so that my note about who these people are survives.
36. As a user whose Target Repository table has no `allowlist` line yet, I want `allowlist add` to add one, so that I don't have to create it first.
37. As a user, I want Allowlist commands on a repository that isn't opted in to fail and point me to `repo add`, so that I don't silently opt a repository in.
38. As a user, I want `repo list` to show every Target Repository with its Allowlist, sorted by name, so that I can check my setup at a glance.
39. As a user, I want `repo list` to mark an empty Allowlist clearly, so that I can see which repositories `--n` will skip.
40. As a user, I want `repo list` to leave out secrets and memory settings, so that its output stays short and safe to paste into a conversation.
41. As a user with nothing opted in, I want `repo list` to tell me how to opt a repository in, so that an empty list isn't a dead end.
42. As a user, I want wrong arguments to these commands to print usage and exit with the usage exit code, so that they behave like the rest of lights-off.
43. As a user, I want `--help` and the usage text to describe `repo add` as opting in, so that the command and the glossary use the same words.
44. As a user, I want the README's Setup section to lead with these commands and keep the TOML as a reference, so that both newcomers and hand-editors are served.
45. As a user running an Autonomous Run, I want a config edit made during the run not to disturb it, so that I can change settings for the next run safely.

## Implementation Decisions

- **Vocabulary.** The commands, messages and help use the glossary terms: a repository is *opted in* or *opted out* and becomes or stops being a **Target Repository**. Users are on its **Allowlist**. "Project" and "trusted users" are not used. The config key stays `allowlist`.
- **Command surface.** A `repo` command with `add`, `remove` and `list`, and an `allowlist` command with `add` and `remove`, are recognised the way `plan` is today. They can't clash with a repository argument, which always contains a `/`. `repo add` takes an optional `--allow` with comma-separated users. `allowlist add` and `allowlist remove` take one repository and one or more users as positionals. The Dashboard and other run flags aren't accepted on these commands.
- **Config-only.** These commands return before secrets loading, preflight, the Dashboard and the run directory. They send no commands through `Env.Exec`, read no `secrets.env` values and touch no cache or state directory.
- **Config editor.** A new piece of the config module edits `config.toml` as text instead of rewriting it from the parsed form:
  - It always parses the whole file first with the existing parser. Any parse error is reported as today (file and line) and nothing is written.
  - Opting in appends a new repository table at the end, with an `allowlist` line, separated from what's above by a blank line.
  - Opting out removes the repository's table and its `.secrets` table: from each header up to the next table header, along with any blank lines that belonged to them.
  - Allowlist edits replace only the `allowlist` line's value and keep any trailing comment. If the table has no `allowlist` line, one is inserted right after the table header.
  - The edited text is parsed again before it is written, as a safety net. If it doesn't parse, that is a Tool failure.
- **Writing.** The new content goes to a temporary file in the same directory, which is then renamed over `config.toml`. An existing file keeps its mode. A missing config directory is created with mode 700 and a missing file with mode 600.
- **Validation.**
  - Repositories use the existing `owner/repo` pattern.
  - Usernames must be GitHub user logins: letters, digits and single hyphens, 1–39 characters, no leading or trailing hyphen. Bot logins are refused. Hand-edited files keep accepting any plain string, as today.
  - No validation reaches the network.
- **Repeats and misses.**
  - Adding a repository or user that's already present succeeds with an "already" message, exit 0. Users are compared ignoring case.
  - `repo add --allow` on an opted-in repository adds the missing users.
  - Removing a missing repository or user fails, exit 1.
  - A multi-user command validates every name and checks every removal before anything is written.
  - Usernames are stored exactly as typed.
- **Warnings.** When an Allowlist is empty after a command, whether it was opted in without users or lost its last user, a one-line warning says `--n` will pick up nothing until someone is added, and that `--ticket` still works with a confirmed override.
- **Opting out with secrets.** After removing the tables, the command prints the removed lines. It lists the secret names that the opted-out repository declared and no remaining Target Repository declares, saying to delete them from `secrets.env`, because lights-off refuses to start while it holds a value nothing declares. It never edits or reads values from `secrets.env`.
- **Listing.** `repo list` prints one line per Target Repository, sorted by name: the repository, then its Allowlist joined by commas, or "(empty allowlist)". With no repositories opted in, it prints a hint pointing to `repo add`.
- **Refusal message.** The "not opted in" refusal names the repository and gives the `repo add <owner>/<repo> --allow=<your-github-username>` command, instead of TOML lines.
- **README.** Setup leads with the commands. The TOML example stays as a reference for hand-editing and for settings the CLI doesn't cover (`implementer_memory`, secrets).
- **Runs in progress.** An Autonomous Run reads the config once, at the start. Because writes are atomic, an edit during a run affects only later runs.
- **No ADR.** None of these decisions is hard to reverse.

## Testing Decisions

- **One seam.** Everything is tested through `lightsoff.Run(args, Env)` with the existing `world` fake. Tests set up a starting `config.toml` (and `secrets.env` where relevant), run a command, and assert on:
  - the exit code
  - stdout and stderr
  - the resulting `config.toml` text and its mode
  - that no commands were sent to the fake world, which proves the commands work offline.
- **Behaviour, not internals.** Tests check what a person sees and what ends up in the file, never the editor's internal steps. A good extra check: after a command, the next run gives the right opt-in result. For example, a `--ticket` run on a newly opted-in repository gets past the opt-in check, and one on an opted-out repository is refused with the new message.
- **Coverage.**
  - Each command's success path.
  - Comments and blank lines preserved around an edit.
  - Trailing comments on `allowlist` lines kept.
  - Tables without an `allowlist` line.
  - Opting out a repository with a `.secrets` table, both when its secrets are declared elsewhere and when they aren't.
  - Repeats and misses, and how each exits.
  - Case-insensitive matching.
  - All-or-nothing multi-user commands.
  - Refused bot logins and malformed names.
  - Unreadable configs left unchanged.
  - Creating a missing directory and file with the right modes, and keeping an existing file's mode.
  - Warnings for an empty Allowlist.
  - `repo list` output, including the empty case.
  - Usage errors exit with the usage exit code.
- **Prior art.** The config tests already drive `Run` with table-driven configs and assert on exit codes, `config.toml:<line>:` messages and an empty command list. The secrets tests show how to set up `secrets.env` and its permissions in the world.

## Out of Scope

- Editing `implementer_memory` or repository secrets from the command line.
- Reading, writing or cleaning up `secrets.env`.
- Checking with GitHub that a repository or user exists.
- Removing a Target Repository's cached checkout or run logs.
- Bot accounts on the Allowlist through the CLI (still possible by editing the file).
- Interactive prompts or confirmations.
- Any change to how eligibility uses the Allowlist.

## Further Notes

- The glossary now defines **Opt in** and **Allowlist**.
- The existing parser deliberately refuses anything outside its subset. The editor depends on that: every file it edits is one the parser fully understood, so line-level edits can't misread structure.
