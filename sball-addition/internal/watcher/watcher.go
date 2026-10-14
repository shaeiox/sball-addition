// Package watcher implements the post-quorate watcher: it waits until the PVM
// socket reports the quorate state and then runs the post-quorate hooks exactly
// once per service start.
//
// The watcher is the piece of sball-addition that lives longest in memory, so
// it is deliberately boring: one goroutine, one lock, one poll loop.
package watcher

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/lock"
	"sball-addition/internal/log"
	"sball-addition/internal/pvm"
	"sball-addition/internal/runner"
	"sball-addition/internal/version"
)

// Sentinel errors, mapped to exit codes by the CLI.
var (
	// ErrLockHeld means another watcher already owns the node.
	ErrLockHeld = errors.New("another watcher is already running")
	// ErrPollTimeout means the node never reached quorate in time.
	ErrPollTimeout = errors.New("timed out waiting for quorate state")
)

// Options configures the watcher.
type Options struct {
	Config       *config.Config
	Logger       *log.Logger
	Once         bool          // single poll, no error when not quorate yet
	Force        bool          // run post-quorate hooks even if RUN_ONCE already done
	PollInterval time.Duration // 0 means "use the config value"
	PollTimeout  time.Duration // 0 means "use the config value"
	// Client allows tests to inject a PVM client; nil builds one from config.
	Client *pvm.Client
}

// Run waits for quorate, runs the post-quorate hooks and returns.
//
// ctx cancellation (SIGTERM/SIGINT) makes Run return nil after cleaning up the
// pid file; a poll timeout returns ErrPollTimeout.
func Run(ctx context.Context, opt Options) error {
	cfg := opt.Config
	if cfg == nil {
		return errors.New("watcher: missing config")
	}
	logger := opt.Logger
	if logger == nil {
		logger = log.NewStderr(nil, log.Info)
	}

	interval := opt.PollInterval
	if interval <= 0 {
		interval = time.Duration(cfg.PostQuorate.PollIntervalSeconds) * time.Second
	}
	timeout := opt.PollTimeout
	if timeout <= 0 {
		timeout = time.Duration(cfg.PostQuorate.PollTimeoutSeconds) * time.Second
	}

	// One watcher per node, enforced by an flock (released even on SIGKILL).
	l, err := lock.Acquire(cfg.Paths.LockFile())
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			holder := lock.Holder(cfg.Paths.LockFile())
			if holder > 0 {
				return fmt.Errorf("%w (pid %d)", ErrLockHeld, holder)
			}
			return ErrLockHeld
		}
		return err
	}
	defer func() { _ = l.Release() }()

	pidPath := cfg.Paths.PidFile()
	if err := writePidFile(pidPath); err != nil {
		logger.Warn("pid_file_failed", log.String("path", pidPath), log.String("error", err.Error()))
	}
	defer func() { _ = os.Remove(pidPath) }()

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// SIGHUP reloads the log file (logrotate) and the hook set.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)

	client := opt.Client
	if client == nil {
		client = pvm.New(cfg.General.SocketHost, cfg.General.SocketPort)
		client.Timeout = dialTimeout(interval)
	}

	hostname := pvm.LocalName()
	logger.Info("watcher_start",
		log.String("component", "watcher"),
		log.String("host", hostname),
		log.Int("pid", os.Getpid()),
		log.String("socket", client.Addr()),
		log.Int("poll_interval_seconds", int(interval.Seconds())),
		log.Int("poll_timeout_seconds", int(timeout.Seconds())),
		log.Bool("once", opt.Once),
	)

	started := time.Now()
	deadline := started.Add(timeout)
	polls := 0

	for {
		select {
		case <-ctx.Done():
			logger.Info("watcher_stop",
				log.String("component", "watcher"),
				log.String("reason", "signal"),
				log.Int("polls", polls),
			)
			return nil
		case <-hup:
			if err := logger.Reopen(); err != nil {
				logger.Warn("log_reopen_failed", log.String("error", err.Error()))
			} else {
				logger.Info("log_reopened", log.String("log_file", logger.Path()))
			}
			continue
		default:
		}

		polls++
		status, err := client.Status()
		switch {
		case err != nil:
			if errors.Is(err, pvm.ErrUnavailable) {
				logger.Debug("poll",
					log.String("component", "watcher"),
					log.Int("polls", polls),
					log.String("state", "down"),
					log.String("error", err.Error()),
				)
			} else {
				logger.Warn("poll",
					log.String("component", "watcher"),
					log.Int("polls", polls),
					log.String("state", "error"),
					log.String("error", err.Error()),
				)
			}
		case status.Quorate:
			logger.Info("quorate",
				log.String("component", "watcher"),
				log.Int("polls", polls),
				log.String("host", hostname),
				log.String("state", status.State),
				log.String("master_id", status.MasterID),
				log.Bool("is_master", status.IsMaster),
				log.Int("waited_ms", int(time.Since(started).Milliseconds())),
			)
			return runPostQuorate(ctx, opt, logger, status)
		default:
			state := status.State
			if status.Local == nil {
				state = "not-in-cluster"
			}
			logger.Debug("poll",
				log.String("component", "watcher"),
				log.Int("polls", polls),
				log.String("state", state),
			)
		}

		if opt.Once {
			logger.Info("watcher_stop",
				log.String("component", "watcher"),
				log.String("reason", "once"),
				log.String("state", "not-quorate"),
			)
			return nil
		}

		if time.Now().After(deadline) {
			logger.Error("poll_timeout",
				log.String("component", "watcher"),
				log.Int("timeout_seconds", int(timeout.Seconds())),
				log.Int("polls", polls),
				log.String("socket", client.Addr()),
			)
			return fmt.Errorf("%w after %s (%d polls)", ErrPollTimeout, timeout, polls)
		}

		select {
		case <-ctx.Done():
			logger.Info("watcher_stop",
				log.String("component", "watcher"),
				log.String("reason", "signal"),
				log.Int("polls", polls),
			)
			return nil
		case <-time.After(interval):
		}
	}
}

func runPostQuorate(ctx context.Context, opt Options, logger *log.Logger, status *pvm.Status) error {
	cfg := opt.Config
	hostname := pvm.LocalName()

	if !cfg.PostQuorate.Enabled {
		logger.Warn("phase_disabled",
			log.String("component", "watcher"),
			log.String("phase", string(hook.PhasePost)),
			log.String("reason", "post-quorate.enabled=false"),
		)
		return nil
	}

	// Re-read the hook directory now: hooks added while sballd was starting are
	// picked up without restarting anything.
	set := hook.LoadPhase(cfg.Paths.PostDir, hook.PhasePost)
	for _, w := range set.Warnings {
		logger.Warn("hook_warning", log.String("component", "watcher"), log.String("warning", w))
	}
	if len(set.Errors) > 0 {
		for _, e := range set.Errors {
			logger.Error("hook_error", log.String("component", "watcher"), log.String("error", e.Error()))
		}
		logger.Error("watcher_stop",
			log.String("component", "watcher"),
			log.String("reason", "invalid-hooks"),
			log.Int("errors", len(set.Errors)),
		)
		return fmt.Errorf("post-quorate hook set is invalid (%d problem(s))", len(set.Errors))
	}

	hooksToRun := set.Enabled()
	env := runner.Env{
		Hostname:     hostname,
		MasterID:     status.MasterID,
		MasterKnown:  status.MasterKnown,
		IsMaster:     status.IsMaster,
		QuorateState: orDefault(status.State, "quorate"),
		Version:      version.Version,
	}

	r := runner.New(runner.Options{
		Phase:          hook.PhasePost,
		Paths:          cfg.Paths,
		DefaultTimeout: cfg.General.DefaultTimeout,
		PhaseOnFailure: cfg.PostQuorate.OnFailure,
		Force:          opt.Force,
		Env:            env,
		Logger:         logger,
	})

	sum := r.Run(ctx, hooksToRun)
	if sum.Empty {
		logger.Info("phase_empty",
			log.String("component", "watcher"),
			log.String("phase", string(hook.PhasePost)),
			log.String("dir", cfg.Paths.PostDir),
		)
	}
	if err := sum.Err(); err != nil {
		logger.Error("watcher_stop",
			log.String("component", "watcher"),
			log.String("reason", "aborted"),
			log.String("error", err.Error()),
		)
		return err
	}
	logger.Info("watcher_stop",
		log.String("component", "watcher"),
		log.String("reason", "done"),
		log.Int("ok", sum.OK()),
		log.Int("failed", len(sum.Failures())),
		log.Int("skipped", sum.Skipped()),
	)
	return nil
}

func dialTimeout(interval time.Duration) time.Duration {
	// Never let a single poll block longer than the poll interval; 5s is the
	// practical upper bound for a local socket.
	d := interval
	if d <= 0 || d > 5*time.Second {
		d = 5 * time.Second
	}
	return d
}

func writePidFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
