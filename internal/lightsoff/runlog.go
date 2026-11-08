package lightsoff

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// openRunDir makes this Autonomous Run's own log directory, named for the
// repository and when the run started, and opens its run.log.
func openRunDir(stateDir, repo string, env Env) (string, *os.File, error) {
	base := filepath.Join(stateDir, "runs")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", nil, fmt.Errorf("making the log directory: %w", err)
	}
	stem := env.Clock.Now().UTC().Format("20060102-150405") + "-" + strings.ReplaceAll(repo, "/", "-")
	// Two runs can start in the same second, and neither may write into the
	// other's directory.
	for i := 1; ; i++ {
		dir := filepath.Join(base, stem)
		if i > 1 {
			dir = fmt.Sprintf("%s-%d", dir, i)
		}
		err := os.Mkdir(dir, 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("making the log directory: %w", err)
		}
		f, err := os.OpenFile(filepath.Join(dir, "run.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return "", nil, fmt.Errorf("opening the run log: %w", err)
		}
		return dir, f, nil
	}
}

// writeAgentLog keeps everything one agent pass was told and produced.
func writeAgentLog(path, sandbox, prompt string, res Result) error {
	var status string
	switch {
	case res.Interrupted:
		status = "interrupted"
	case res.TimedOut:
		status = "stopped at its time limit"
	default:
		status = fmt.Sprintf("exited %d", res.ExitCode)
	}
	text := fmt.Sprintf("sandbox: %s\n%s\n\n--- prompt ---\n%s\n\n--- stdout ---\n%s\n\n--- stderr ---\n%s\n",
		sandbox, status, prompt, res.Stdout, res.Stderr)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return fmt.Errorf("writing the agent log: %w", err)
	}
	return nil
}

// teeWriters copies stdout and stderr into the run log, under one lock so that
// each line lands whole in both places.
func teeWriters(stdout, stderr io.Writer, log io.Writer) (io.Writer, io.Writer) {
	return lockWriters(io.MultiWriter(stdout, log), io.MultiWriter(stderr, log))
}
