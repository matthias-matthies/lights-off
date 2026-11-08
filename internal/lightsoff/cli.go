// Package lightsoff is the lights-off command. Run is its only test seam: the
// outside world reaches it solely through Env.
package lightsoff

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const usage = `usage: lights-off <owner>/<repo> --ticket=<ticket> [--no-dashboard]
       lights-off <owner>/<repo> --n=<count> [--no-dashboard]
       lights-off plan <owner>/<repo> [--no-dashboard]
       lights-off repo add <owner>/<repo>      opt in a repository
       lights-off repo remove <owner>/<repo>   opt out a repository
       lights-off --version`

// Exit codes.
const (
	exitOK      = 0
	exitFailure = 1
	exitUsage   = 2
)

// Command is one invocation of gh, git or sbx. Secret values travel only in
// Stdin and Env, because Args are visible in process lists.
type Command struct {
	Name  string
	Args  []string
	Stdin string   // piped to the command; nothing if empty
	Env   []string // added to the command's environment, as NAME=value

	Timeout time.Duration   // kill the command after this long; no limit if zero
	Stop    <-chan struct{} // kill the command once this closes; never if nil

	// Attach hands the person's terminal to the command, for a session they
	// drive themselves. Nothing is captured, so its Result has only ExitCode.
	Attach bool
}

// Result is what a finished Command produced. A non-zero ExitCode is a normal
// Result; an error from Env.Exec means the command could not be run at all.
type Result struct {
	Stdout      string
	Stderr      string
	ExitCode    int
	TimedOut    bool // Timeout passed and the command was killed
	Interrupted bool // Stop closed and the command was killed
}

// Clock lets the long waits be driven without real time passing.
type Clock interface {
	Now() time.Time
	Sleep(time.Duration)
}

// Env is everything lights-off needs from the outside world.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer

	// Stdin and IsTerminal are for the --ticket confirmation, and IsTerminal
	// also for the Planning Session a person drives. StdoutIsTerminal decides
	// whether a Dashboard starts; when nil, it follows IsTerminal. HostMemory is
	// the host's total memory in bytes, which sets how many tickets run at once.
	Stdin            io.Reader
	IsTerminal       func() bool
	StdoutIsTerminal func() bool
	HostMemory       func() (uint64, error)

	// OpenURL opens a URL in the person's browser. A failure of the browser is
	// never a Tool failure, so callers ignore its error.
	OpenURL func(string) error
	// Listen starts the Dashboard's listener. It is net.Listen when nil.
	Listen func(network, address string) (net.Listener, error)

	// Interrupt starts catching Ctrl+C, which otherwise ends lights-off at
	// once, and returns a channel closed by the first one. Only an Autonomous
	// Run asks, once it is under way: a prompt or a person's session needs
	// Ctrl+C left alone.
	Interrupt func() <-chan struct{}

	// OnEvent, if set, receives every structured ticket event as it is
	// reported. The Dashboard subscribes here. It may be called from several
	// Ticket Attempts at once.
	OnEvent func(Event)

	Exec  func(Command) (Result, error)
	Clock Clock

	ConfigDir string // holds config.toml
	CacheDir  string // holds the kit and each Target Repository's checkout
	StateDir  string // holds each Autonomous Run's log directory

	SbxConfigDir string // holds sbx's credentials.yaml

	// Version is what --version prints. A release build sets it; it is "dev"
	// when empty.
	Version string
}

// autonomousRun is what every Ticket Attempt of one Autonomous Run shares.
type autonomousRun struct {
	logDir string
	stop   <-chan struct{} // closed by Ctrl+C

	// hostCheckout is held while anything changes the host checkout, which
	// every Ticket Attempt of the run shares: sbx create --clone copies whatever
	// it has at HEAD, and adds a remote to it, as sbx rm removes one.
	hostCheckout *sync.Mutex
}

type verb int

const (
	verbRun verb = iota
	verbPlan
	verbRepoAdd
	verbRepoRemove
)

type options struct {
	verb        verb
	repo        string
	ticket      int
	n           int
	noDashboard bool
	version     bool
}

// Run executes the command line in args and returns the process exit code.
func Run(args []string, env Env) int {
	// Tickets running at once share these, and each status line must stay whole.
	env.Stdout, env.Stderr = lockWriters(env.Stdout, env.Stderr)
	opts, err := parseArgs(args, env.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return exitOK
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: %v\n%s\n", err, usage)
		return exitUsage
	}
	if opts.version {
		version := env.Version
		if version == "" {
			version = "dev"
		}
		fmt.Fprintf(env.Stdout, "lights-off %s\n", version)
		return exitOK
	}
	if opts.verb == verbRepoAdd {
		if err := addRepo(opts.repo, env); err != nil {
			fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	if opts.verb == verbRepoRemove {
		if err := removeRepo(opts.repo, env); err != nil {
			fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
			return exitFailure
		}
		return exitOK
	}

	cfg, err := loadConfig(env.ConfigDir)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
		return exitFailure
	}
	if !cfg.optedIn(opts.repo) {
		fmt.Fprintf(env.Stderr, "lights-off: %s is not opted in. To opt it in, run:\n\n    lights-off repo add %s\n",
			opts.repo, opts.repo)
		return exitFailure
	}

	if opts.verb == verbPlan {
		if err := openPlanningSession(opts.repo, cfg, env, !opts.noDashboard); err != nil {
			fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
			return exitFailure
		}
		return exitOK
	}
	values, err := preflight(opts, cfg, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
		return exitFailure
	}
	// The --ticket confirmation comes first, so that a refusal leaves no run and
	// no Dashboard, and so that a confirmed one starts the Dashboard only after
	// the person answered.
	var only ticket
	if opts.n == 0 {
		if only, err = startTicket(opts, cfg.repos[opts.repo], env); err != nil {
			fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
			return exitFailure
		}
	}
	if dashboard := startDashboard(env, opts.repo, dashboardCommand(opts), cfg.repos[opts.repo].allowlist, !opts.noDashboard); dashboard != nil {
		defer dashboard.Close()
		// The Dashboard subscribes to the same structured events the Reporter
		// turns into status lines. A test or another subscriber still sees
		// every one of them.
		onEvent := env.OnEvent
		env.OnEvent = func(e Event) {
			dashboard.publish(e)
			if onEvent != nil {
				onEvent(e)
			}
		}
	}
	stop := env.Interrupt()
	logDir, runLog, err := openRunDir(env.StateDir, opts.repo, env)
	if err != nil {
		fmt.Fprintf(env.Stderr, "lights-off: %v\n", err)
		return exitFailure
	}
	defer runLog.Close()
	env.Stdout, env.Stderr = teeWriters(env.Stdout, env.Stderr, runLog)
	fmt.Fprintf(env.Stdout, "lights-off: logs in %s\n", logDir)
	done, announced := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(announced)
		announceInterrupt(stop, done, env)
	}()
	// Nothing may write once Run has returned, nor after the run log closes.
	defer func() { close(done); <-announced }()

	run := autonomousRun{logDir: logDir, stop: stop, hostCheckout: &sync.Mutex{}}
	if opts.n != 0 {
		return runSeveral(opts, cfg.repos[opts.repo], values, run, env)
	}
	report := &reporter{out: env.Stdout, onEvent: env.OnEvent}
	a := newAttempt(opts.repo, only, cfg.repos[opts.repo], values, run, env, report)
	o, claimed := a.claim()
	if claimed {
		o = a.run()
	}
	var outcomes []outcome
	if o.ticket != 0 {
		outcomes = append(outcomes, o)
	}
	summary := printSummary(env.Stdout, outcomes, logDir)
	report.report(Event{Kind: EventSummary, Summary: summary})
	return o.code
}

// lockedWriter writes each call whole, whichever goroutine makes it.
type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// lockWriters shares one lock between stdout and stderr, because they are
// often the same terminal.
func lockWriters(stdout, stderr io.Writer) (io.Writer, io.Writer) {
	mu := &sync.Mutex{}
	return lockedWriter{mu, stdout}, lockedWriter{mu, stderr}
}

// startTicket lets --ticket override failed checks, but only with a person's
// explicit confirmation. Overriding a check never weakens the Merge Gate.
func startTicket(opts options, repo repoConfig, env Env) (ticket, error) {
	sh := shell{exec: env.Exec}
	a, err := sh.assess(opts.repo, opts.ticket, repo.allowlist)
	if err != nil {
		return ticket{}, err
	}
	if a.closed {
		return ticket{}, fmt.Errorf("#%d is closed, so there is nothing to start", opts.ticket)
	}
	if a.mayStart() {
		return a.ticket, nil
	}
	fmt.Fprintf(env.Stderr, "lights-off: #%d failed these checks:\n", opts.ticket)
	for _, failed := range a.failed {
		fmt.Fprintf(env.Stderr, "  - %s\n", failed)
	}
	// Without a terminal nobody is there to answer, and piped input could say
	// yes on a person's behalf.
	if !env.IsTerminal() {
		return ticket{}, errors.New("not started: no terminal is attached for a person to confirm overriding them")
	}
	fmt.Fprintf(env.Stderr, "Start #%d anyway? [y/N] ", opts.ticket)
	answer, _ := bufio.NewReader(env.Stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return a.ticket, nil
	}
	return ticket{}, errors.New("not started")
}

// preflight refuses to start unless the secrets are safe and sbx will use them,
// before lights-off has touched a ticket or created a sandbox.
func preflight(opts options, cfg config, env Env) (secrets, error) {
	values, err := loadSecrets(env.ConfigDir, cfg, opts.repo, slices.Concat(implementerGrants, reviewerGrants))
	if err != nil {
		return nil, err
	}
	sh := shell{exec: env.Exec}
	mounts, err := sh.sandboxMounts()
	if err != nil {
		return nil, err
	}
	// What lights-off itself is about to hand to sbx counts too.
	mounts = append(mounts, checkoutDir(env.CacheDir, opts.repo), kitDir(env.CacheDir))
	if err := checkNotMounted(secretsPath(env.ConfigDir), mounts); err != nil {
		return nil, err
	}
	if err := sh.checkValueStdin(cfg.repos[opts.repo].secrets); err != nil {
		return nil, err
	}
	if err := mergeBindings(env.SbxConfigDir); err != nil {
		return nil, err
	}
	return values, nil
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("lights-off", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.IntVar(&opts.ticket, "ticket", 0, "")
	fs.IntVar(&opts.n, "n", 0, "")
	fs.BoolVar(&opts.noDashboard, "no-dashboard", false, "")
	fs.BoolVar(&opts.version, "version", false, "")

	// The flag package stops at the first positional, and the repository comes
	// first, so parse around positionals.
	var positionals []string
	for rest := args; ; {
		if err := fs.Parse(rest); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				fmt.Fprintln(stderr, usage)
			}
			return opts, err
		}
		if fs.NArg() == 0 {
			break
		}
		positionals = append(positionals, fs.Arg(0))
		rest = fs.Args()[1:]
	}

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if set["version"] {
		if len(set) > 1 || len(positionals) > 0 {
			return opts, errors.New("--version takes nothing else")
		}
		return opts, nil
	}
	if len(positionals) > 0 && positionals[0] == "repo" {
		if set["ticket"] || set["n"] || set["no-dashboard"] {
			return opts, errors.New("repo takes none of --ticket, --n or --no-dashboard")
		}
		switch {
		case len(positionals) == 3 && positionals[1] == "add":
			opts.verb = verbRepoAdd
			opts.repo = positionals[2]
		case len(positionals) == 3 && positionals[1] == "remove":
			opts.verb = verbRepoRemove
			opts.repo = positionals[2]
		default:
			return opts, errors.New("expected repo add or repo remove <owner>/<repo>")
		}
		if !repoPattern.MatchString(opts.repo) {
			return opts, errors.New("expected one <owner>/<repo>")
		}
		return opts, nil
	}
	if len(positionals) > 0 && positionals[0] == "plan" {
		opts.verb = verbPlan
		positionals = positionals[1:]
		if set["ticket"] || set["n"] {
			return opts, errors.New("plan takes neither --ticket nor --n")
		}
	} else if set["ticket"] == set["n"] {
		return opts, errors.New("pass exactly one of --ticket and --n")
	}
	if len(positionals) != 1 || !repoPattern.MatchString(positionals[0]) {
		return opts, errors.New("expected one <owner>/<repo>")
	}
	opts.repo = positionals[0]
	if set["ticket"] && opts.ticket <= 0 {
		return opts, errors.New("--ticket must be a positive issue number")
	}
	if set["n"] && opts.n <= 0 {
		return opts, errors.New("--n must be a positive count")
	}
	return opts, nil
}
