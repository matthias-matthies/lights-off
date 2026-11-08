# Spike 0001: `dsh` unattended in `sbx`

Ticket: [#1](https://github.com/matthias-matthies/lights-off/issues/1)
Date: 2026-11-06
Status: macOS path proven; the Linux path still needs a smoke test.

## Environment

| Piece | Version |
|---|---|
| Host | macOS 27.2, Apple silicon, 8 GB RAM, 8 CPUs |
| `sbx` | v0.50.1 (v0.51.0 available) |
| `dsh` | 0.2.2-rc.1 |
| Node in the kit | 24.22.2 |
| Model route | `deepseek-official`, `deepseek-flash`, through a proxy-managed `DEEPSEEK_API_KEY` |
| Target repo | a throwaway local Git repository |

The working kit is [`kits/dsh/`](../../kits/dsh/spec.yaml). It extends the
built-in `shell` image, installs Node 24.22.2 and dsh 0.2.2-rc.1, and bundles
the `implement`, `pr`, `code-review` and `tdd` skills.

## Acceptance criteria

| Criterion | Result |
|---|---|
| Kit starts a sandbox with Node 24.9+, a pinned dsh, the required skills and DeepSeek network access | Pass. `sbx kit validate --offline ./kits/dsh` passed; a sandbox created from the kit reported Node 24.22.2 and dsh 0.2.2-rc.1. |
| `dsh --profile headless` completes a job unattended, with exit codes, output and approval settings recorded | Pass. See [Headless contract](#headless-contract). |
| DeepSeek is proxy-managed and dsh authenticates without the agent reading the real key | Pass. The agent sees `DEEPSEEK_API_KEY=proxy-managed`; the sbx proxy injects the real key into `x-api-key`. `sbx secret set-custom` was not needed. |
| `code-review` runs unattended, or a bundled-prompt fallback is chosen | Pass. It ran in 48 s, used two subagents, and returned a Verdict. No fallback needed. |
| An agent can return a parseable JSON result | Pass with a caveat. The model sometimes wraps the JSON in prose or a code fence, so the answer must be extracted rather than parsed whole. |
| dsh memory measured; limits confirmed or corrected; reservation recorded | Pass. dsh peaked at about 0.28 GB RSS. Limits are corrected to 2 GB/1 GB. Limits are guest caps, not host reservations. |
| Whether removing a sandbox removes its scoped secrets | It normally does, including the per-sandbox policy. A keychain failure can remove the sandbox and leave the secret behind; see [Sandbox lifecycle](#sandbox-lifecycle). |
| `sbx create --clone`, `sbx exec`, stop and restart from a script with no terminal | Pass. |
| `sbx` support confirmed on macOS and Debian | Partial. macOS is confirmed. `sbx` documents Ubuntu 24.04+ as its Linux support, not Debian. No Linux host was available, so the Ubuntu path is not live-tested. |

## The kit

- `kits/dsh/spec.yaml` is a schema v2 sandbox kit with `extends: shell`.
- It sets `DSH_PERMISSION_MODE=danger-full-access`, `GIT_TERMINAL_PROMPT=0`
  and `GH_PROMPT_DISABLED=1`.
- It declares the `deepseek` service with `proxyManaged: true`, injecting into
  `api.deepseek.com` with header `x-api-key` and format `%s`. The header matters:
  dsh calls DeepSeek's Anthropic-compatible Messages endpoint, so the key is an
  `x-api-key`, not a bearer token.
- It allows `api.deepseek.com`, Node and npm, GitHub, and Ubuntu/Debian package
  hosts. The kit's policy showed 10 allow rules in the sandbox.
- `setup.install` checks for `git`, `gh`, `jq`, `curl`, `tar` and `xz`, installs
  Node 24.22.2, then installs dsh with
  `npm install -g --prefix /usr/local --ignore-scripts @deepseek-ai/dsh@0.2.2-rc.1`.
  The postinstall scripts are not needed for headless work; bash, skill and
  subagent tools all ran without them.

## Headless contract

The invocation that worked unattended:

```sh
dsh --profile headless \
  --permission-mode danger-full-access \
  --approval-policy never \
  "<job>"
```

`dsh` 0.2.2-rc.1 also accepts `--json`, `--session-id <id>` and `--add-dir <path>`.

- **No TTY is needed.** All runs went through `sbx exec` without `-t`.
- **Exit codes:** 0 when the turn completes; 1 for any other outcome (missing
  key, aborted turn, driver failure). `sbx exec` returns 125 when sbx itself
  fails (for example, an unknown sandbox) and 127 for a missing command.
- **Default output:** the final answer alone on stdout; reasoning and
  `[llm-deepseek-api-key] model listing ...` diagnostics on stderr.
- **`--json` output:** newline-delimited events on stdout. The answer is
  `final.text`; a failed run still ends with `final` and sets
  `reason.kind = "error"`. An out-of-turn failure also writes `dsh: <code>:
  <message>` to stderr and exits 1.
- **Approval settings are load-bearing.** With the default
  `workspace-write` + `ask`, a headless write outside the workspace silently
  failed and the model still answered `DONE`; the file did not exist. With
  `--permission-mode danger-full-access --approval-policy never`, the write
  succeeded. The outer sandbox is the isolation boundary, so the kit defaults
  to that mode. The base profile maps `DSH_PERMISSION_MODE=danger-full-access`
  to approval policy `never`; the explicit flags are still recommended.
- **JSON is not schema-enforced.** A prompt ending "end with only a JSON
  object" produced pure JSON in one run and a fenced ```` ```json ```` block in
  another. A prompt asking for a JSON object "as its final answer" produced
  prose with the object inside. Lights-off must extract the first/last balanced
  object and treat a missing or invalid object as a crash.

## Credentials

The proxy-managed service path works end to end:

1. The kit declares the `deepseek` service and injects it into
   `api.deepseek.com` with `x-api-key`.
2. `~/.config/sbx/credentials.yaml` needs an approval binding:

   ```yaml
   bindings:
     deepseek:
       apiKey:
         domains: [api.deepseek.com]
   ```

3. Store the value scoped to the sandbox. `sbx secret set` reads the value from
   its stdin prompt, so it never appears in argv or logs:

   ```sh
   printf '%s' "$DEEPSEEK_API_KEY" | sbx secret set deepseek --sandbox spike-dsh
   ```

   The service name may be one the kit declares even though `sbx secret set
   --help` only lists built-in services.
4. Inside the sandbox, `DEEPSEEK_API_KEY` is the 13-character sentinel
   `proxy-managed`. The agent never sees the real key. No `~/.dsh/.credentials.yaml`
   exists in the sandbox; dsh resolves the sentinel from the environment and the
   proxy swaps in the real key on the wire.

`sbx secret set-custom` is not needed. Its documented `--value-stdin` flag is
not present in sbx 0.50.1 (`unknown flag`), so if it ever becomes the fallback,
the value must go through the prompt instead. The custom-secret route also
avoids the binding file, but the service route is the cleaner one.

Not tested end to end: the GitHub credential. The `shell` base kit inherits a
`github` service and sets `GH_TOKEN`, but a local kit needs a binding for it
too. Without a binding and a stored secret, `gh auth status` reports the token
as invalid. Lights-off must add the `github` binding and store a token scoped to
the sandbox before the Implementer can push or open a pull request. The same
applies to the Reviewer's separate read-only token.

## Skills

Bundled from `mattpocock/skills` at commit
`4ae2317ddb22b046e26455e4f9c9b3aaaf62212d`:

- `implement`
- `pr`
- `code-review`
- `tdd`

Two upstream behaviours needed changes or workarounds:

- **`implement` is hidden from the model upstream.** It sets
  `disable-model-invocation: true`. In a headless run the catalog listed only
  `code-review`, `pr` and `tdd`, and calling the skill returned
  `Error: skill "implement" is not available for model invocation`. The kit's
  vendored copy removes that one frontmatter line; after that the skill loaded
  and completed a small ticket in 15 s, committing the change and returning
  pure JSON.
- **`code-review` asks for a fixed point and a spec.** Lights-off supplies both
  in the prompt, so it does not stop to ask. It used two subagents and returned
  a Verdict in 48 s. `pr` and `tdd` are present but were not exercised in a full
  PR flow.

`implement` still tells the model to run `code-review` before committing.
Lights-off's Implementer prompt must explicitly override that, because the
independent Reviewer runs later. In the spike, the override worked and no
review ran inside the Implementer's run.

## Memory

Measurements inside a 2 GB sandbox, sampling all descendants of the dsh process:

| Run | Wall time | Peak RSS |
|---|---|---|
| Simple JSON answer | 2.5 s | 277 MB |
| `code-review` with two subagents | 45 s | 270 MB |

- A 3 GB `bytearray` allocation in a 2 GB sandbox raised `MemoryError`, so the
  limit is enforced.
- The limit is a guest-RAM cap, not a host reservation. On an 8 GB Mac with a
  4 GB sandbox and a 2 GB sandbox, the host had about 1.1 GB of swap in use.
  Stopping the 2 GB sandbox released only about 0.6 GB. The host can overcommit
  and swap, so the 75% concurrency rule remains the guard.
- The spec now starts at 2 GB for the Implementer and 1 GB for the Reviewer,
  with 4 GB as the recommended per-repository override for heavy builds. dsh
  itself is not the constraint; the repository's toolchain is.

## Sandbox lifecycle

- `sbx create --clone --name spike-dsh --memory 2g ./kits/dsh <repo>` created
  the sandbox from a script with no terminal.
- The sandbox's workspace is the host repository path
  (`/Users/.../.spike/target`), and `WORKSPACE_DIR` matches it.
- The host repository gained a `sandbox-spike-dsh` remote. `git fetch
  sandbox-spike-dsh` imported `main`, `feature/add` and `feature/hello`.
- `sbx stop spike-dsh` followed by `sbx exec` started it again; `/tmp` files and
  the workspace survived, and the clone remote was restored.
- `sbx rm` warns that commits made in a `--clone` sandbox are lost unless
  fetched first. Fetched branches are mirrored under
  `refs/sandboxes/<name>/*`; recover one with
  `git branch LOCAL refs/sandboxes/<name>/BRANCH`.
- With normal host keychain access, `sbx rm` removed the sandbox, its scoped
  `deepseek` secret, and its per-sandbox policy. When the calling process could
  not reach the OS keychain (in this environment, the harness's file sandbox),
  `sbx rm` still removed the sandbox but exited 1 with
  `delete credential: Keychain Error. (100001)` and left the secret orphaned.
  Lights-off must check the result and retry `sbx secret rm <service> --sandbox
  <name>`.

## Platforms

- **macOS:** confirmed on macOS 27.2, Apple silicon, with sbx 0.50.1.
- **Linux:** sbx's install documentation supports Ubuntu 24.04+ on x86_64 and
  arm64, with KVM enabled. Debian is not listed, and Docker says it does not
  test or support Ubuntu derivatives. No Linux host was available, so Ubuntu
  was not live-tested either.
- The spec, ticket #1 and ticket #12 were corrected from Debian to Ubuntu
  24.04+.

## Corrections made

- `.scratch/lights-off/spec.md`: platform Linux target is Ubuntu 24.04+, not
  Debian; Implementer/Reviewer memory defaults are 2 GB/1 GB; dsh approval
  flags are explicit; credential bindings are required; `x-api-key` replaces
  the generic bearer assumption; JSON extraction replaces whole-output parsing;
  memory limits are not reservations.
- Ticket #12: retitled "Smoke test and release on macOS and Ubuntu", with the
  Debian acceptance criteria corrected.
- Ticket #1: platform criterion corrected and the completed criteria checked.

## Remaining work

1. Run the same kit and smoke test on Ubuntu 24.04+ (KVM enabled). This is the
   only unmet acceptance criterion.
2. Verify the GitHub credential binding plus a scoped token end to end
   (`gh auth status`, push, open a pull request), then the Reviewer's read-only
   token.
3. Decide how lights-off obtains and merges the `~/.config/sbx/credentials.yaml`
   bindings for `deepseek`, `github` and the reviewer service without
   overwriting unrelated entries.
4. Keep the vendored-skill patch (`implement` made model-invocable) when the
   skill pin moves.
