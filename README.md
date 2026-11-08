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

Download the latest release:

```sh
gh release download --repo matthias-matthies/lights-off --pattern '*darwin_arm64.tar.gz'
tar -xzf lights-off_*_darwin_arm64.tar.gz
mv lights-off ~/.local/bin/    # or anywhere on your PATH
lights-off --version
```

The binary isn't notarized. If you download it through a browser instead, clear the quarantine flag with `xattr -d com.apple.quarantine lights-off`.

## Setup

Lights-off works only on repositories you opt in to, in `~/.config/lights-off/config.toml`:

```toml
[repos."owner/repo"]
allowlist = ["your-github-user"]   # whose ready-for-agent labels count
```

Secrets go in `~/.config/lights-off/secrets.env`, which must have mode `600`:

| Secret | Used by |
|---|---|
| `DEEPSEEK_API_KEY` | Implementer and Reviewer |
| `GITHUB_TOKEN` | Implementer, Planning Session, and lights-off itself for merging |
| `GITHUB_REVIEWER_TOKEN` (read-only) | Reviewer |

If anything is missing, lights-off refuses to start and says what to add: for a repository that isn't opted in, it prints the config lines.

An `--n` run only picks up tickets whose `ready-for-agent` label was added by someone on the allowlist. Agents see only a ticket's main text, never its comments.

## Building from source

```sh
go build -ldflags "-X main.version=$(git describe --tags)" ./cmd/lights-off
```

## License

[MIT](LICENSE). The skills bundled in `kits/dsh` are by Matt Pocock, under the MIT license in [`kits/dsh/SKILLS-LICENSE`](kits/dsh/SKILLS-LICENSE).
