// Command lights-off takes tickets on an opted-in Target Repository from start
// to merge, using sandboxed agents.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/matthias-matthies/lights-off/internal/lightsoff"
)

// version is set by the release build, with -ldflags "-X main.version=v0.1.0".
var version string

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "lights-off:", err)
		os.Exit(1)
	}
	os.Exit(lightsoff.Run(os.Args[1:], lightsoff.Env{
		Stdout:           os.Stdout,
		Stderr:           os.Stderr,
		Stdin:            os.Stdin,
		IsTerminal:       stdinIsTerminal,
		StdoutIsTerminal: stdoutIsTerminal,
		OpenURL:          openURL,
		Interrupt:        catchInterrupt,
		HostMemory:       hostMemory,
		Exec:             runCommand,
		Clock:            systemClock{},
		ConfigDir:        filepath.Join(home, ".config", "lights-off"),
		CacheDir:         filepath.Join(home, ".cache", "lights-off"),
		StateDir:         filepath.Join(home, ".local", "state", "lights-off"),

		SbxConfigDir: filepath.Join(home, ".config", "sbx"),

		Version: version,
	}))
}

// ctrlC closes on the first Ctrl+C, once catchInterrupt has been called.
var ctrlC = make(chan struct{})

// catchInterrupt turns the first Ctrl+C into a close of ctrlC, so that lights-off
// can stop its agents and give tickets back. A second one quits at once. By then
// the first may have given some tickets back already, or all of them, and
// nothing here knows which, so the message says only what may be left.
func catchInterrupt() <-chan struct{} {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-signals
		close(ctrlC)
		<-signals
		fmt.Fprintln(os.Stderr, "lights-off: quit before cleanup finished: sandboxes may still be running, and claimed tickets may still carry agent-in-progress; recover any that do with --ticket")
		os.Exit(130)
	}()
	return ctrlC
}

// killedCommandGrace bounds the wait after a limit kills a command, because a
// child of it can hold its output open and Run would otherwise wait for that too.
const killedCommandGrace = 10 * time.Second

// runCommand gives commands only the stdin they ask for and disables prompts,
// because a prompt in an unattended run would otherwise hang.
func runCommand(c lightsoff.Command) (lightsoff.Result, error) {
	if c.Attach {
		return runAttached(c)
	}
	ctx := context.Background()
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	if c.Stop != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		go func() {
			select {
			case <-c.Stop:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	cmd := exec.CommandContext(ctx, c.Name, c.Args...)
	if c.Timeout > 0 || c.Stop != nil {
		cmd.WaitDelay = killedCommandGrace
	}
	// Its own process group keeps a Ctrl+C in the terminal from reaching the
	// command, so that lights-off alone decides what stops, and cleanup after
	// it isn't cut short.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdin = strings.NewReader(c.Stdin)
	cmd.Env = append(append(os.Environ(), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0"), c.Env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := lightsoff.Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		return res, nil
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		res.Interrupted = true
		return res, nil
	}
	if errors.Is(err, exec.ErrWaitDelay) {
		return res, nil // it exited 0, and only something it left behind held its output open
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		res.ExitCode = exit.ExitCode()
		return res, nil
	}
	return res, err
}

// runAttached gives the command this process's terminal. A Ctrl+C that reaches
// this process as well is caught rather than ignored, because an ignored signal
// stays ignored in the child and the session would lose it.
func runAttached(c lightsoff.Command) (lightsoff.Result, error) {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	cmd := exec.Command(c.Name, c.Args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), c.Env...)
	err := cmd.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return lightsoff.Result{ExitCode: exit.ExitCode()}, nil
	}
	return lightsoff.Result{}, err
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// Sleep ends early on Ctrl+C, so that a run waiting for CI stops at once.
func (systemClock) Sleep(d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctrlC:
	}
}

var memTotal = regexp.MustCompile(`(?m)^MemTotal:\s+(\d+) kB$`)

func hostMemory() (uint64, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, err
		}
		return strconv.ParseUint(strings.TrimSpace(string(out)), 10, 64)
	}
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	match := memTotal.FindSubmatch(data)
	if match == nil {
		return 0, errors.New("no MemTotal in /proc/meminfo")
	}
	kb, err := strconv.ParseUint(string(match[1]), 10, 64)
	return kb * 1024, err
}
