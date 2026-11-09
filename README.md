# lights-off

A small command-line tool that takes GitHub tickets from start to merge using sandboxed agents, so small, low-risk changes can land without you.

- **Planning Session:** `lights-off plan <owner>/<repo>` opens Claude Code in an [`sbx`](https://github.com/docker/sbx-releases) sandbox, with the repository cloned and a new Planning Branch checked out. You plan, and push and merge the branch yourself.
- **Autonomous Run:** `lights-off <owner>/<repo> --n=<count>` works through up to that many eligible tickets, in parallel as far as host memory allows. `--ticket=<number>` works on one named ticket. An Implementer completes each ticket and opens a pull request. Once CI is green, a fresh Reviewer returns a Verdict. The Merge Gate then merges low-risk changes, hands fixable problems back for up to three Iterations, and leaves everything else as an open pull request labelled `ready-for-human`.
- **Dashboard:** a live view in your browser while a run or session is going. Turn it off with `--no-dashboard`.

Agents never get read access to your secrets. Credentials reach a sandbox only through `sbx`'s host-side proxy. See [`docs/adr/`](docs/adr/) for the safety rules and [`GLOSSARY.md`](GLOSSARY.md) for the terms.

## Requirements

- macOS 14+ on Apple silicon
- [`sbx`](https://github.com/docker/sbx-releases) 0.51.0 or later, plus `gh` and `git`

## Install

With Homebrew:

```sh
brew install --cask matthias-matthies/tap/lights-off
```

This also installs `sbx` (from `docker/tap`) and `gh`. Upgrade with `brew upgrade lights-off`.

Or download the latest release yourself:

```sh
gh release download --repo matthias-matthies/lights-off --pattern '*darwin_arm64.tar.gz'
tar -xzf lights-off_*_darwin_arm64.tar.gz
mv lights-off ~/.local/bin/    # or anywhere on your PATH
lights-off --version
```

The binary isn't notarized. If you download it through a browser instead, clear the quarantine flag with `xattr -d com.apple.quarantine lights-off`. The Homebrew cask clears it for you.

## Setup

Lights-off works only on repositories you opt in to. Opt one in with its first Allowlist users:

```sh
lights-off repo add owner/repo --allow=alice,bob
```

Or opt it in first and add users later:

```sh
lights-off repo add owner/repo
lights-off allowlist add owner/repo alice bob
```

These commands create or edit `~/.config/lights-off/config.toml`, keeping your comments, blank lines and other settings. The first adds:

```toml
[repos."owner/repo"]
allowlist = ["alice", "bob"]   # whose ready-for-agent labels count
```

Running `repo add --allow=carol` on a repository that is already opted in adds the users it names that are missing, so the command from a refusal is safe to re-run. An `--n` run picks up nothing until someone is on the Allowlist. `--ticket` still works with an empty Allowlist, after you confirm the override.

To take users off a Target Repository's Allowlist, run:

```sh
lights-off allowlist remove owner/repo alice bob
```

Names match ignoring case, so `Alice` removes an entry stored as `alice`. If any user isn't on the Allowlist, the command names them and changes nothing. Removing the last user leaves `allowlist = []`.

To opt a Target Repository out, run:

```sh
lights-off repo remove owner/repo
```

There is no confirmation prompt. It deletes the repository's table and its `[repos."owner/repo".secrets]` table, leaves the rest of the file alone, and prints the removed lines so you can paste them back. If it declared a secret that no remaining Target Repository declares, the command names it: delete that value from `secrets.env` before the next run, or lights-off will refuse to start. The repository's cached checkout and run logs are left alone.

The file stays the reference for hand-editing and for settings the CLI doesn't cover, such as `implementer_memory` and repository secrets:

```toml
[repos."owner/repo"]
allowlist = ["your-github-user"]   # whose ready-for-agent labels count
implementer_memory = "4g"          # optional; defaults to 2g

[repos."owner/repo".secrets]
STRIPE_KEY = ["api.stripe.com"]    # domains the sbx proxy injects it into
```

Secrets go in `~/.config/lights-off/secrets.env`, which must have mode `600`:

| Secret | Used by |
|---|---|
| `DEEPSEEK_API_KEY` | Implementer and Reviewer |
| `GITHUB_TOKEN` | Implementer, Planning Session, and lights-off itself for merging |
| `GITHUB_REVIEWER_TOKEN` (read-only) | Reviewer |

If anything is missing, lights-off refuses to start and says what to add: for a repository that isn't opted in, it prints the `lights-off repo add <owner>/<repo> --allow=<your-github-username>` command.

An `--n` run only picks up tickets whose `ready-for-agent` label was added by someone on the allowlist. Agents see only a ticket's main text, never its comments.

## Building from source

```sh
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/lights-off
```

## License

[MIT](LICENSE). The skills bundled in `kits/dsh` are by Matt Pocock, under the MIT license in [`kits/dsh/SKILLS-LICENSE`](kits/dsh/SKILLS-LICENSE).
