package lightsoff

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Starting memory limits. Set explicitly because sbx otherwise gives each
// sandbox 50% of host memory.
const (
	implementerMemory = "2g"
	reviewerMemory    = "1g"
	// planningMemory is for Claude Code, which a person drives with nothing
	// else of lights-off running beside it.
	planningMemory = "4g"
)

// agentLimit is how long one agent may run before it counts as a crash.
const agentLimit = 60 * time.Minute

// sandbox is one created sandbox and what's stored for it, each secret as the
// arguments that identify it to sbx secret rm.
type sandbox struct {
	name    string
	stored  [][]string
	stopped bool
}

// sandboxes is the sandbox driver for one Target Repository checkout.
type sandboxes struct {
	shell
	kit       string
	workspace string
	ctrlC     <-chan struct{} // kills a running agent
}

var unsafeNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes text safe for a sandbox name. Different texts can share a slug.
func slug(text string) string {
	return strings.Trim(unsafeNameChars.ReplaceAllString(strings.ToLower(text), "-"), "-")
}

// sandboxName is unique per create, because a kept sandbox from an earlier
// attempt at the same ticket may still exist.
func sandboxName(repo string, ticket int, role string, iteration int, now time.Time) string {
	_, name, _ := strings.Cut(repo, "/")
	return fmt.Sprintf("lo-%.30s-%d-%s-%d-%d", slug(name), ticket, role, iteration, now.Unix())
}

func (s sandboxes) create(name, memory string) error {
	_, err := s.run("sbx", "create", "--clone", "--name", name, "--memory", memory, s.kit, s.workspace)
	return err
}

// deliver stores each secret scoped to the sandbox, for its proxy to inject. sbx
// picks up a secret stored after creation without a restart.
func (s sandboxes) deliver(sb *sandbox, grants []grant, repoSecrets []repoSecret, values secrets) error {
	for _, g := range grants {
		if err := s.pipeSecret(values[g.variable], "secret", "set", g.service, "--sandbox", sb.name); err != nil {
			return err
		}
		sb.stored = append(sb.stored, []string{g.service})
	}
	for _, secret := range repoSecrets {
		if err := s.deliverRepoSecret(sb, secret, values[secret.name]); err != nil {
			return err
		}
	}
	return nil
}

// deliverRepoSecret uses a custom secret, because the kit declares no service
// for it. The kit's network policy doesn't cover its domains either. sbx only
// applies --env when a sandbox starts, so a sandbox that already exists is told
// the placeholder separately; the name and sandbox are shell-safe by validation,
// and the placeholder is what the agent is meant to see.
func (s sandboxes) deliverRepoSecret(sb *sandbox, secret repoSecret, value string) error {
	if _, err := s.run("sbx", "policy", "allow", "network", "--sandbox", sb.name, strings.Join(secret.domains, ",")); err != nil {
		return err
	}
	placeholder := "lights-off-" + sb.name + "-" + secret.name
	args := []string{"secret", "set-custom", "--sandbox", sb.name}
	for _, domain := range secret.domains {
		args = append(args, "--host", domain)
	}
	args = append(args, "--env", secret.name, "--placeholder", placeholder, "--value-stdin")
	if err := s.pipeSecret(value, args...); err != nil {
		return err
	}
	sb.stored = append(sb.stored, []string{"--placeholder", placeholder})

	export := fmt.Sprintf("echo 'export %s=%s' >> /etc/sandbox-persistent.sh", secret.name, placeholder)
	_, err := s.run("sbx", "exec", "-w", s.workspace, sb.name, "sh", "-c", export)
	return err
}

// pipeSecret runs sbx with a value on stdin. Its stderr stays out of the error,
// because a tool's complaint about a value can quote the value.
func (s shell) pipeSecret(value string, args ...string) error {
	res, err := s.exec(Command{Name: "sbx", Args: args, Stdin: value})
	if err != nil {
		return fmt.Errorf("sbx %s: %w", strings.Join(args[:2], " "), err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("sbx %s exited %d", strings.Join(args, " "), res.ExitCode)
	}
	return nil
}

// Exit codes sbx exec can return for an agent.
const (
	sbxExecFailed = 125 // sbx itself failed, so the agent says nothing about the work
	exitKilled    = 137 // SIGKILL, which in a memory-capped sandbox is the OOM killer
)

// runAgent runs one headless dsh turn and returns its final answer. A crash comes
// back as a handBack naming the agent. The approval flags are load-bearing:
// without them a headless dsh silently fails writes outside the workspace and
// still reports success.
//
// An agent past its limit has its sandbox stopped too, because killing sbx exec
// on the host can leave the agent running inside.
//
// Everything the agent was told and produced goes to logPath, whatever happened.
func (s sandboxes) runAgent(sb *sandbox, agent, prompt, logPath string) (string, error) {
	sb.stopped = false // sbx exec starts a stopped sandbox again, keeping its workspace
	res, err := s.exec(Command{
		Name: "sbx",
		Args: []string{"exec", "-w", s.workspace, sb.name,
			"dsh", "--profile", "headless", "--permission-mode", "danger-full-access", "--approval-policy", "never",
			prompt},
		Timeout: agentLimit,
		Stop:    s.ctrlC,
	})
	if err == nil || res.Interrupted {
		if logErr := writeAgentLog(logPath, sb.name, prompt, res); logErr != nil && err == nil {
			return "", logErr
		}
	}
	if err != nil {
		return "", fmt.Errorf("sbx exec: %w", err)
	}
	switch {
	case res.TimedOut:
		if err := s.stop(sb); err != nil {
			return "", err
		}
		return "", crashed(agent, fmt.Sprintf("it passed the %.0f-minute limit and was stopped", agentLimit.Minutes()))
	case res.ExitCode == sbxExecFailed:
		return "", fmt.Errorf("sbx exec exited %d: %s", res.ExitCode, lastLine(res.Stderr))
	case res.ExitCode == exitKilled:
		return "", crashed(agent, fmt.Sprintf("it was killed, most likely out of memory (exit %d)", res.ExitCode))
	case res.ExitCode != 0:
		return "", crashed(agent, fmt.Sprintf("it exited %d: %s", res.ExitCode, lastLine(res.Stderr)))
	}
	return res.Stdout, nil
}

// stop releases a sandbox's memory and keeps its contents.
func (s shell) stop(sb *sandbox) error {
	if sb.stopped {
		return nil
	}
	if _, err := s.run("sbx", "stop", sb.name); err != nil {
		return err
	}
	sb.stopped = true
	return nil
}

// remove passes --force because sbx rm otherwise asks for confirmation, which an
// unattended run can't answer. sbx rm can exit non-zero having removed the
// sandbox but not its scoped secrets, when it can't reach the OS keychain, so a
// failure means nothing until sbx ls says whether the sandbox is still there.
func (s shell) remove(sb *sandbox) error {
	_, err := s.run("sbx", "rm", "--force", sb.name)
	if err == nil {
		return nil
	}
	names, lsErr := s.run("sbx", "ls", "-q")
	if lsErr != nil || slices.Contains(strings.Fields(names), sb.name) {
		return err
	}
	return s.removeSecrets(sb)
}

// removeSecrets retries the cleanup sbx rm didn't finish. That cleanup may have
// got partway, and removing a secret that's already gone can fail, so a failure
// only counts if sbx still lists secrets for the sandbox.
func (s shell) removeSecrets(sb *sandbox) error {
	var failed []string
	for _, id := range sb.stored {
		args := slices.Concat([]string{"secret", "rm"}, id, []string{"--sandbox", sb.name, "--force"})
		if _, err := s.run("sbx", args...); err != nil {
			failed = append(failed, "sbx "+strings.Join(args, " "))
		}
	}
	if len(failed) == 0 {
		return nil
	}
	left, err := s.run("sbx", "secret", "ls", "--sandbox", sb.name, "-q")
	if err == nil && strings.TrimSpace(left) == "" {
		return nil
	}
	return fmt.Errorf("sandbox removed but its secrets remain; remove them with: %s", strings.Join(failed, "; "))
}
