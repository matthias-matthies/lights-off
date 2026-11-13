# dsh sandbox kit

An `sbx` schema v3 mixin for DeepSeek Harness (`dsh`) running headless in a
sandbox, added to sbx's built-in `shell` workload. It is the kit the lights-off spike proved out; see
[`docs/spikes/0001-dsh-unattended-in-sbx.md`](../../docs/spikes/0001-dsh-unattended-in-sbx.md)
for the measurements and caveats.

## What it provides

- on top of the `shell` workload:
  - Node 24.22.2 from nodejs.org
  - `@deepseek-ai/dsh@0.2.2-rc.1`, installed with `--ignore-scripts` (headless
    work does not need the optional native postinstall scripts)
  - the `implement`, `pr`, `code-review` and `tdd` skills under
    `/home/agent/.agents/skills`
- `DSH_PERMISSION_MODE=danger-full-access`, `GIT_TERMINAL_PROMPT=0` and
  `GH_PROMPT_DISABLED=1` (args pinned by `enum`, since v3 sets environment
  variables only through args) so a headless run does not stall on
  approval prompts. The sbx sandbox is the isolation boundary.
- a proxy-managed `deepseek` service: the sandbox sees
  `DEEPSEEK_API_KEY=proxy-managed`, and sbx injects the real key into the
  `x-api-key` header on `api.deepseek.com`. dsh uses DeepSeek's
  Anthropic-compatible Messages endpoint, so this header is required.
- a proxy-managed `github` service for `gh` and Git over HTTPS, injected as
  `Authorization: Bearer` on `api.github.com`, `github.com`
  and `raw.githubusercontent.com`. v3 kits have no `scheme`
  shorthand, so Git over HTTPS no longer gets Basic auth with
  `x-access-token`; Docker's own `gh` example kit uses a bearer header for
  `github.com` too. The `shell` kit declares no credentials, and a token
  stored for one sandbox overrides any other.
- network allow rules for DeepSeek, Node/npm, GitHub and Ubuntu/Debian package
  hosts.

## Prerequisites

1. `sbx` 0.51.0 or later, signed in, and Docker Desktop for the first build
   (see below). Target-Repository-specific secrets need its
   `secret set-custom --value-stdin`.
2. A DeepSeek key.
3. A credential binding in `~/.config/sbx/credentials.yaml`. Local kits need an
   approval entry; without it sbx starts the sandbox with the credential
   withheld.

   ```yaml
   bindings:
     deepseek:
       apiKey:
         domains: [api.deepseek.com]
   ```

   Merge this into an existing file rather than replacing it. Lights-off merges
   this entry and a `github` one itself before creating a sandbox; this step is
   for using the kit by hand.

4. The key stored in sbx, scoped to the sandbox you are about to create. The
   value is read from the prompt on stdin, so it never reaches argv or logs:

   ```sh
   printf '%s' "$DEEPSEEK_API_KEY" | sbx secret set deepseek --sandbox my-dsh
   ```

   The secret can be stored after creation; the proxy picks it up without a
   restart.

## Build and use

`sbx` builds a v3 source kit with Docker Desktop's daemon and caches the
result as `sbx-kit-src:dsh-<hash>`. Later creates use the cache without Docker
Desktop. It builds again when the kit changes, and after 7 days without use,
when sbx removes the cached build. Without Docker Desktop, such a create fails
with `Cannot connect to the Docker daemon`.

Validate (this builds too, so it needs Docker Desktop; `--offline` can't build
v3 source kits):

```sh
sbx kit validate ./kits/dsh
```

The skills are copied into the image at `/usr/share/lights-off/skills` and an
install hook copies them to `/home/agent/.agents/skills` as the agent user.
They are not declared with `agent-skill@1`, which would also mount the host's
shared skills store into an unattended sandbox.

Create a sandbox on a private clone of a host Git repository:

```sh
sbx create --clone --name my-dsh --memory 2g --kit ./kits/dsh shell /path/to/repo
```

Run one headless job. The kit already sets `DSH_PERMISSION_MODE`, but passing
the flags explicitly is recommended:

```sh
sbx exec -w /path/to/repo my-dsh \
  dsh --profile headless \
    --permission-mode danger-full-access \
    --approval-policy never \
    "Implement ticket #42 and end with a JSON result."
```

`dsh` writes the final answer to stdout and exits 0 when the turn completes;
any other outcome exits 1. Use `--json` for a newline-delimited event stream;
the answer is then `final.text`. The model may wrap JSON in prose or a code
fence, so extract the object rather than parsing the whole output.

The sandbox's workspace is the host repository path. The host repository gets a
`sandbox-<name>` Git remote; fetch it before `sbx rm`, because commits made
inside a clone sandbox are otherwise lost.

## Skills

The four skill directories are vendored from
[`mattpocock/skills`](https://github.com/mattpocock/skills) at commit
`4ae2317ddb22b046e26455e4f9c9b3aaaf62212d`, under the MIT license in
[`SKILLS-LICENSE`](SKILLS-LICENSE).

One deliberate edit: upstream `implement` sets
`disable-model-invocation: true`, which hides it from the model. The vendored
copy removes that line, because a headless run has no human to invoke the skill.
Keep this patch when updating the skill pin.

`implement` also instructs the model to run `code-review`; lights-off's prompt
must override that, because the independent Reviewer runs in a fresh sandbox
after CI.

## Tested

| Host | sbx | dsh | Result |
|---|---|---|---|
| macOS 27.2, Apple silicon | 0.50.1 | 0.2.2-rc.1 | Kit created, headless JSON task, `code-review` and `implement` all ran unattended |
| macOS, Apple silicon, on `shell-image` | 0.51.0 | 0.2.2-rc.1 | Kit created; Node, dsh, gh and the `GH_TOKEN` placeholder present. No agent task or GitHub call run yet |
| macOS, Apple silicon, v2 kit on `shell-image` | 0.53.0 | — | Fails: `failed to apply kit to sandbox` (seen with a kit of the same shape in another project). Fixed by the v3 mixin |
| macOS, Apple silicon, v3 mixin on `shell` | 0.53.0 | 0.2.2-rc.1 | Not yet tested |
| Ubuntu 24.04+ | — | — | Not yet tested; sbx documents Ubuntu as its Linux platform, not Debian |
