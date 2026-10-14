// Package cli implements the sball-addition command line interface.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/log"
	"sball-addition/internal/proc"
	"sball-addition/internal/pvm"
	"sball-addition/internal/runner"
	"sball-addition/internal/version"
)

// Exit codes. They are part of the interface: scripts (notably
// sballd-wrapper) branch on them.
const (
	ExitOK       = 0
	ExitFailure  = 1 // a hook failed / phase aborted
	ExitUsage    = 2 // bad command line
	ExitConfig   = 3 // configuration is missing or invalid
	ExitNotFound = 4 // hook or log file not found, hook set invalid
	ExitLocked   = 5 // another watcher holds the lock
	ExitTimeout  = 6 // watcher poll timeout
)

// App holds the process wide CLI state.
type App struct {
	Root       string
	ConfigPath string
	LogLevel   string
	LogFile    string
	Socket     string
	NoLog      bool

	Stdout io.Writer
	Stderr io.Writer

	cfg *config.Config
	log *log.Logger
}

// globalFlags are accepted before or after the sub-command, which keeps the
// wrapper and interactive use both comfortable.
func (a *App) extractGlobals(args []string) ([]string, error) {
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, value, hasValue := splitFlag(arg)
		switch name {
		case "--root":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, errors.New("--root requires a value")
				}
				i++
				value = args[i]
			}
			a.Root = value
		case "--config":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, errors.New("--config requires a value")
				}
				i++
				value = args[i]
			}
			a.ConfigPath = value
		case "--log-level":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, errors.New("--log-level requires a value")
				}
				i++
				value = args[i]
			}
			a.LogLevel = value
		case "--log-file":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, errors.New("--log-file requires a value")
				}
				i++
				value = args[i]
			}
			a.LogFile = value
		case "--socket":
			if !hasValue {
				if i+1 >= len(args) {
					return nil, errors.New("--socket requires a value")
				}
				i++
				value = args[i]
			}
			a.Socket = value
		case "--no-log":
			a.NoLog = true
		default:
			rest = append(rest, arg)
		}
	}
	return rest, nil
}

func splitFlag(arg string) (name, value string, hasValue bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", "", false
	}
	if idx := strings.Index(arg, "="); idx >= 0 {
		return arg[:idx], arg[idx+1:], true
	}
	return arg, "", false
}

// Run is the entry point used by main().
func Run(args []string) int {
	a := &App{Stdout: os.Stdout, Stderr: os.Stderr}
	if a.Root == "" {
		a.Root = config.DefaultRoot()
	}

	// --help/--version before any command.
	if len(args) > 0 {
		switch args[0] {
		case "-h", "--help":
			args = []string{"help"}
		case "-V", "--version":
			args = []string{"version"}
		}
	}

	rest, err := a.extractGlobals(args)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitUsage
	}
	if len(rest) == 0 {
		a.printUsage(a.Stdout)
		return ExitOK
	}

	cmd, cmdArgs := rest[0], rest[1:]
	switch cmd {
	case "list", "ls":
		return a.cmdList(cmdArgs)
	case "run":
		return a.cmdRun(cmdArgs)
	case "run-phase":
		return a.cmdRunPhase(cmdArgs)
	case "status", "st":
		return a.cmdStatus(cmdArgs)
	case "log", "logs":
		return a.cmdLog(cmdArgs)
	case "enable":
		return a.cmdToggle(cmdArgs, true)
	case "disable":
		return a.cmdToggle(cmdArgs, false)
	case "install-hook":
		return a.cmdInstallHook(cmdArgs)
	case "uninstall-hook":
		return a.cmdUninstallHook(cmdArgs)
	case "validate":
		return a.cmdValidate(cmdArgs)
	case "watcher":
		return a.cmdWatcher(cmdArgs)
	case "version":
		return a.cmdVersion(cmdArgs)
	case "help":
		return a.cmdHelp(cmdArgs)
	case "completion":
		a.printUsage(a.Stdout)
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "%s: unknown command %q\n\n", version.Name, cmd)
		a.printUsage(a.Stderr)
		return ExitUsage
	}
}

func (a *App) printUsage(w io.Writer) {
	fmt.Fprintf(w, `%s %s - hook system for the sballd (PVM) service

usage: %s [global options] <command> [command options]

commands:
  list [--phase=<phase>] [--json]
        show every hook with its metadata in execution order
  run <hook-name> [--phase=<phase>] [--force] [--no-state] [--quiet]
        run a single hook (respects TIMEOUT/RETRY/TARGET/RUN_ONCE)
  run-phase <phase> [--force] [--quiet] [--json]
        run every hook of a phase (used by sballd-wrapper)
  status [--json]
        show sballd, watcher, cluster and hook state
  log [--follow] [--hook=<name>] [--phase=<phase>] [--since=<time>]
      [--level=<level>] [--limit=<n>] [--pretty]
        print the JSON log (raw by default so it can be piped to jq)
  enable <hook-name> [--phase=<phase>]
  disable <hook-name> [--phase=<phase>]
        flip the ENABLED field in the hook header
  install-hook <file> --phase=<phase> [--order=<n>] [--force]
        validate and install a hook script
  uninstall-hook <hook-name> [--phase=<phase>] [--keep-state]
        remove a hook and its RUN_ONCE state
  validate [--phase=<phase>]
        check the configuration and every hook header
  watcher [--foreground] [--once] [--poll-interval=<s>] [--poll-timeout=<s>]
        wait for the quorate state and run the post-quorate hooks
  version [--json]
  help [<command>]

global options:
  --root=<dir>        installation root (default $SBALL_ADDITION_ROOT or /)
  --config=<file>     configuration file (default <root>/etc/sball-addition/sball-addition.conf)
  --log-level=<lvl>   debug | info | warn | error
  --log-file=<file>   override the JSON log destination
  --socket=<h:p>      override the PVM XML-RPC socket
  --no-log            do not write to the log file (stdout/stderr only)

exit codes:
  0 ok   1 hook failure   2 usage   3 configuration   4 not found / invalid hooks
  5 watcher lock held     6 quorate poll timeout
`, version.Name, version.Version, version.Name)
}

func (a *App) cmdHelp(args []string) int {
	f := newFlagSet("help", args)
	if err := f.Parse(); err != nil {
		return a.usageError("help", err)
	}
	a.printUsage(a.Stdout)
	return ExitOK
}

func (a *App) cmdVersion(args []string) int {
	f := newFlagSet("version", args)
	asJSON := f.Bool("json", false, "machine readable output")
	if err := f.Parse(); err != nil {
		return a.usageError("version", err)
	}
	if *asJSON {
		a.printJSON(map[string]any{
			"name":       version.Name,
			"version":    version.Version,
			"commit":     version.Commit,
			"build_date": version.BuildDate,
			"go_version": runtime.Version(),
		})
		return ExitOK
	}
	fmt.Fprintln(a.Stdout, version.String())
	return ExitOK
}

func (a *App) usageError(cmd string, err error) int {
	fmt.Fprintf(a.Stderr, "%s %s: %v\n", version.Name, cmd, err)
	fmt.Fprintf(a.Stderr, "run '%s help' for usage\n", version.Name)
	return ExitUsage
}

func (a *App) fail(format string, args ...any) int {
	fmt.Fprintf(a.Stderr, "%s: %s\n", version.Name, fmt.Sprintf(format, args...))
	return ExitFailure
}

// config loads (once) and validates the configuration.
func (a *App) config() (*config.Config, int) {
	if a.cfg != nil {
		return a.cfg, ExitOK
	}
	root := a.Root
	if strings.TrimSpace(root) == "" {
		root = config.DefaultRoot()
	}
	path := a.ConfigPath
	if path == "" {
		path = config.DefaultConfigPath(root)
	}
	cfg, warnings, err := config.Load(path, root)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return nil, ExitConfig
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "%s: warning: %s\n", version.Name, w)
	}
	if a.LogLevel != "" {
		cfg.General.LogLevel = a.LogLevel
	}
	if a.LogFile != "" {
		cfg.General.LogFile = a.LogFile
		cfg.Paths.LogFile = a.LogFile
	}
	if a.Socket != "" {
		host, port, err := net.SplitHostPort(a.Socket)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%s: --socket: %v (expected host:port)\n", version.Name, err)
			return nil, ExitUsage
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%s: --socket: %q is not a port\n", version.Name, port)
			return nil, ExitUsage
		}
		cfg.General.SocketHost = host
		cfg.General.SocketPort = p
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(a.Stderr, "%s: invalid configuration: %v\n", version.Name, err)
		return nil, ExitConfig
	}
	a.cfg = cfg
	return cfg, ExitOK
}

// logger builds the JSON logger, honouring --no-log and falling back to
// stderr when the log file cannot be opened.
func (a *App) logger(cfg *config.Config) (*log.Logger, int) {
	if a.log != nil {
		return a.log, ExitOK
	}
	level, err := log.ParseLevel(cfg.General.LogLevel)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return nil, ExitConfig
	}
	path := cfg.Paths.LogFile
	if a.NoLog {
		path = ""
	}
	l, err := log.Open(log.Options{Path: path, Level: level})
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: warning: %v (logging to stderr)\n", version.Name, err)
	}
	a.log = l
	return l, ExitOK
}

func (a *App) printJSON(v any) {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(a.Stderr, "%s: encode json: %v\n", version.Name, err)
	}
}

// signalContext returns a context cancelled by SIGTERM/SIGINT.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
}

// hooksFor loads one or both phase sets.
func (a *App) hooksFor(cfg *config.Config, phases []hook.Phase) []*hook.Set {
	out := make([]*hook.Set, 0, len(phases))
	for _, p := range phases {
		out = append(out, hook.LoadPhase(cfg.Paths.Dir(string(p)), p))
	}
	return out
}

func (a *App) phasePolicy(cfg *config.Config, phase hook.Phase) string {
	if phase == hook.PhasePre {
		return cfg.PreQuorate.OnFailure
	}
	return cfg.PostQuorate.OnFailure
}

func (a *App) phaseEnabled(cfg *config.Config, phase hook.Phase) bool {
	if phase == hook.PhasePre {
		return cfg.PreQuorate.Enabled
	}
	return cfg.PostQuorate.Enabled
}

// queryPVM returns a cluster snapshot; a non-nil error means the socket was
// not usable, which is normal before sballd starts.
func (a *App) queryPVM(cfg *config.Config) (*pvm.Status, error) {
	client := pvm.New(cfg.General.SocketHost, cfg.General.SocketPort)
	client.Timeout = 3 * time.Second
	return client.Status()
}

// clusterEnv builds the hook environment, degrading gracefully while sballd is
// down (that is exactly the pre-quorate situation).
func (a *App) clusterEnv(cfg *config.Config, lg *log.Logger) runner.Env {
	env := runner.Env{
		Hostname:     pvm.LocalName(),
		QuorateState: "unknown",
		Version:      version.Version,
	}
	status, err := a.queryPVM(cfg)
	if err != nil {
		if errors.Is(err, pvm.ErrUnavailable) {
			env.QuorateState = "down"
			lg.Debug("pvm_unavailable", log.String("socket", cfg.General.SocketHost+":"+
				strconv.Itoa(cfg.General.SocketPort)))
		} else {
			lg.Warn("pvm_query_failed", log.String("error", err.Error()))
		}
		return env
	}
	env.MasterID = status.MasterID
	env.MasterKnown = status.MasterKnown
	env.IsMaster = status.IsMaster
	if status.State != "" {
		env.QuorateState = status.State
	}
	return env
}

// relative renders "2s ago" style ages.
func relative(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	return proc.FormatDuration(d) + " ago"
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
