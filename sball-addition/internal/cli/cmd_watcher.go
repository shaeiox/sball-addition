package cli

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"

	"sball-addition/internal/log"
	"sball-addition/internal/proc"
	"sball-addition/internal/version"
	"sball-addition/internal/watcher"
)

func (a *App) cmdWatcher(args []string) int {
	f := newFlagSet("watcher", args)
	foreground := f.Bool("foreground", false, "run in the foreground (systemd mode)", "f")
	once := f.Bool("once", false, "poll once and exit (no daemon)")
	pollInterval := f.Int("poll-interval", 0, "override post-quorate.poll_interval_seconds")
	pollTimeout := f.Int("poll-timeout", 0, "override post-quorate.poll_timeout_seconds")
	force := f.Bool("force", false, "run post-quorate hooks even when RUN_ONCE is already recorded")
	if err := f.Parse(); err != nil {
		return a.usageError("watcher", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}

	if !*foreground && !*once {
		return a.daemonizeWatcher(cfg.Paths.LogFile, *pollInterval, *pollTimeout, *force)
	}

	ctx, cancel := signalContext()
	defer cancel()

	opt := watcher.Options{Config: cfg, Logger: lg, Once: *once, Force: *force}
	if *pollInterval > 0 {
		opt.PollInterval = time.Duration(*pollInterval) * time.Second
	}
	if *pollTimeout > 0 {
		opt.PollTimeout = time.Duration(*pollTimeout) * time.Second
	}

	err := watcher.Run(ctx, opt)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, watcher.ErrLockHeld):
		fmt.Fprintf(a.Stderr, "%s: %v (only one watcher may run per node)\n", version.Name, err)
		return ExitLocked
	case errors.Is(err, watcher.ErrPollTimeout):
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitTimeout
	default:
		lg.Error("watcher_error", log.String("component", "watcher"), log.String("error", err.Error()))
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
}

// daemonizeWatcher re-executes "watcher --foreground" in a new session so the
// wrapper and systemd can hand the node over to sballd immediately.
func (a *App) daemonizeWatcher(logFile string, pollInterval, pollTimeout int, force bool) int {
	exe, err := os.Executable()
	if err != nil {
		return a.fail("cannot determine own executable: %v", err)
	}
	argv := []string{"watcher", "--foreground"}
	if a.Root != "" {
		argv = append(argv, "--root="+a.Root)
	}
	if a.ConfigPath != "" {
		argv = append(argv, "--config="+a.ConfigPath)
	}
	if a.LogLevel != "" {
		argv = append(argv, "--log-level="+a.LogLevel)
	}
	if a.LogFile != "" {
		argv = append(argv, "--log-file="+a.LogFile)
	}
	if a.Socket != "" {
		argv = append(argv, "--socket="+a.Socket)
	}
	if a.NoLog {
		argv = append(argv, "--no-log")
	}
	if pollInterval > 0 {
		argv = append(argv, fmt.Sprintf("--poll-interval=%d", pollInterval))
	}
	if pollTimeout > 0 {
		argv = append(argv, fmt.Sprintf("--poll-timeout=%d", pollTimeout))
	}
	if force {
		argv = append(argv, "--force")
	}

	devnull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return a.fail("open %s: %v", os.DevNull, err)
	}
	defer devnull.Close()

	cmd := exec.Command(exe, argv...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, devnull, devnull
	cmd.Env = os.Environ()
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return a.fail("start watcher: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()

	// Give the child a moment: if the lock is held or the config is broken it
	// exits immediately and we would rather report that than pretend success.
	time.Sleep(300 * time.Millisecond)
	if !proc.Alive(pid) {
		fmt.Fprintf(a.Stderr, "%s: watcher exited immediately, see %s\n", version.Name, logFile)
		return ExitFailure
	}
	fmt.Fprintf(a.Stdout, "watcher started (pid %d)\n", pid)
	return ExitOK
}
