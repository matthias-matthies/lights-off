# Agents never get read access to secrets

Secrets live in `~/.config/lights-off/secrets.env` on the host and reach a sandbox only through `sbx`'s host-side proxy, which adds the credential to outgoing requests while the agent sees a stand-in value. Secrets that can't be injected this way, such as a database password a test reads directly, are refused rather than delivered, so tests that need them must run in CI. Do not "fix" this with `sbx create -e`, `--env-file` or a kit's OAuth `passthrough: true`, because each of those puts a real value where an agent can read it.

## Consequences

Agents can't read a secret, but they can still use it on any request to its declared domains, so credential scope is limited per sandbox: the Reviewer gets a read-only GitHub token, and the Implementer gets a separate one. The values remain in plaintext on the host, in a file lights-off requires to be `0600` and refuses to expose through a sandbox mount.
