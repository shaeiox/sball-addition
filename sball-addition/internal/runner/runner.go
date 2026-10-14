// Package runner executes hooks with timeout, retry, RUN_ONCE, TARGET and
// ON_FAIL semantics, emitting one JSON log record per lifecycle event.
package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/log"
)

// Skip reasons reported in the "skip" log event and by the tests.
const (
	SkipDisabled    = "disabled"
	SkipRunOnceDone = "run-once-already-done"
	SkipNotExec     = "not-executable"
	SkipNotMaster   = "target-master-not-master"
	SkipFilter      = "filtered-out"
	SkipPhaseOff    = "phase-disabled"
	SkipDryRun      = "dry-run"
)

// Environment variable names exported to every hook.
const (
	EnvPhase     = "SBALL_HOOK_PHASE"
	EnvName      = "SBALL_HOOK_NAME"
	EnvHost      = "SBALL_HOSTNAME"
	EnvMasterID  = "SBALL_MASTER_ID"
	EnvIsMaster  = "SBALL_IS_MASTER"
	EnvQuorate   = "SBALL_QUORATE_STATE"
	EnvLogFile   = "SBALL_LOG_FILE"
	EnvHookPath  = "SBALL_HOOK_PATH"
	EnvHookDir   = "SBALL_HOOK_DIR"
	EnvStateFile = "SBALL_STATE_FILE"
	EnvStateDir  = "SBALL_STATE_DIR"
	EnvRuntime   = "SBALL_RUNTIME_DIR"
	EnvCommonLib = "SBALL_COMMON_LIB"
	EnvRunID     = "SBALL_RUN_ID"
	EnvAttempt   = "SBALL_RETRY_ATTEMPT"
	EnvRetries   = "SBALL_RETRY_MAX"
	EnvVersion   = "SBALL_ADDITION_VERSION"
)

// Env is the cluster context injected into hooks.
type Env struct {
	Hostname     string
	MasterID     string
	MasterKnown  bool
	IsMaster     bool
	QuorateState string
	Version      string
}

// Options configures a Runner.
type Options struct {
	Phase          hook.Phase
	Paths          config.Paths
	DefaultTimeout int
	PhaseOnFailure string // abort | continue (from the phase config section)
	Force          bool   // ignore RUN_ONCE markers
	DryRun         bool   // resolve and report what would run, but do not execute
	Env            Env
	Logger         *log.Logger
	Stream         bool      // mirror hook stdout/stderr to Stdout/Stderr
	Stdout         io.Writer // used when Stream is true
	Stderr         io.Writer
	Now            func() time.Time
	SkipLastRun    bool   // do not touch the last-run registry (dry runs/tests)
	LastRunFile    string // defaults to Paths.LastRunFile()
}

// Runner executes hooks.
type Runner struct {
	opt   Options
	log   *log.Logger
	now   func() time.Time
	runID string
}

// New builds a Runner.
func New(opt Options) *Runner {
	if opt.Logger == nil {
		opt.Logger = log.NewStderr(nil, log.Info)
	}
	if opt.Stdout == nil {
		opt.Stdout = os.Stdout
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	if opt.LastRunFile == "" {
		opt.LastRunFile = opt.Paths.LastRunFile()
	}
	return &Runner{opt: opt, log: opt.Logger, now: nowOr(opt.Now), runID: NewRunID()}
}

func nowOr(f func() time.Time) func() time.Time {
	if f != nil {
		return f
	}
	return time.Now
}

// NewRunID returns the identifier shared by every record of one phase run.
func NewRunID() string {
	return fmt.Sprintf("%s-%d", time.Now().UTC().Format("20060102T150405Z"), os.Getpid())
}

// RunID returns this runner's run identifier.
func (r *Runner) RunID() string { return r.runID }

// Summary aggregates the outcome of one phase.
type Summary struct {
	Phase     hook.Phase
	RunID     string
	Started   time.Time
	Duration  time.Duration
	Results   []*Result
	Aborted   bool
	AbortHook string
	Empty     bool
}

// OK returns the number of hooks that ran successfully.
func (s *Summary) OK() int {
	n := 0
	for _, r := range s.Results {
		if !r.Skipped && r.Err == nil {
			n++
		}
	}
	return n
}

// Failures returns the results that failed.
func (s *Summary) Failures() []*Result {
	var out []*Result
	for _, r := range s.Results {
		if !r.Skipped && r.Err != nil {
			out = append(out, r)
		}
	}
	return out
}

// Skipped returns the number of skipped hooks.
func (s *Summary) Skipped() int {
	n := 0
	for _, r := range s.Results {
		if r.Skipped {
			n++
		}
	}
	return n
}

// Ran returns the number of hooks that were actually executed.
func (s *Summary) Ran() int {
	n := 0
	for _, r := range s.Results {
		if !r.Skipped {
			n++
		}
	}
	return n
}

// Err returns a non-nil error when the phase must be treated as failed, which
// is exactly the case when a hook with ON_FAIL=abort failed.
func (s *Summary) Err() error {
	if s.Aborted {
		return fmt.Errorf("%s aborted: hook %q failed with ON_FAIL=abort", s.Phase, s.AbortHook)
	}
	return nil
}

// Result is the outcome of a single hook.
type Result struct {
	Hook       *hook.Meta
	Skipped    bool
	SkipReason string
	Attempts   int
	ExitCode   int
	TimedOut   bool
	Duration   time.Duration
	Err        error
	Stdout     string
	Stderr     string
	Started    time.Time
}

// Failed reports whether the hook ran and failed.
func (r *Result) Failed() bool { return !r.Skipped && r.Err != nil }

// Status renders "OK", "FAIL" or "SKIP".
func (r *Result) Status() string {
	switch {
	case r.Skipped:
		return "SKIP"
	case r.Err != nil:
		return "FAIL"
	default:
		return "OK"
	}
}

// Run executes the hooks in order.
func (r *Runner) Run(ctx context.Context, hooks []*hook.Meta) *Summary {
	sum := &Summary{Phase: r.opt.Phase, RunID: r.runID, Started: r.now()}
	r.log.Info("phase_start",
		log.String("phase", string(r.opt.Phase)),
		log.String("run_id", r.runID),
		log.String("host", r.opt.Env.Hostname),
		log.Int("hooks", len(hooks)),
		log.String("dir", r.opt.Paths.Dir(string(r.opt.Phase))),
	)

	if len(hooks) == 0 {
		sum.Empty = true
	}

	for _, h := range hooks {
		if err := ctx.Err(); err != nil {
			sum.Aborted = true
			sum.AbortHook = h.Name
			r.log.Error("phase_abort",
				log.String("phase", string(r.opt.Phase)),
				log.String("hook", h.Name),
				log.String("run_id", r.runID),
				log.String("reason", "interrupted"),
			)
			break
		}
		res := r.RunOne(ctx, h)
		sum.Results = append(sum.Results, res)
		if res.Failed() && h.EffectiveOnFail(r.opt.PhaseOnFailure) == hook.OnFailAbort {
			sum.Aborted = true
			sum.AbortHook = h.Name
			r.log.Error("phase_abort",
				log.String("phase", string(r.opt.Phase)),
				log.String("hook", h.Name),
				log.String("run_id", r.runID),
				log.Int("exit", res.ExitCode),
				log.String("reason", "on-fail-abort"),
			)
			break
		}
	}

	sum.Duration = r.now().Sub(sum.Started)
	r.log.Info("phase_finish",
		log.String("phase", string(r.opt.Phase)),
		log.String("run_id", r.runID),
		log.Int("ok", sum.OK()),
		log.Int("failed", len(sum.Failures())),
		log.Int("skipped", sum.Skipped()),
		log.Millis("duration_ms", sum.Duration),
		log.Bool("aborted", sum.Aborted),
	)
	return sum
}

// RunOne executes a single hook, applying every skip rule and the retry policy.
func (r *Runner) RunOne(ctx context.Context, h *hook.Meta) *Result {
	res := &Result{Hook: h, Started: r.now()}
	timeout := h.EffectiveTimeout(r.opt.DefaultTimeout)

	// --- skip rules -------------------------------------------------------
	if !h.Enabled {
		return r.skip(h, res, SkipDisabled, log.Info)
	}
	if !h.Executable {
		return r.skip(h, res, SkipNotExec, log.Warn)
	}
	if h.RunOnce && !r.opt.Force {
		statePath := r.opt.Paths.StateFile(string(h.Phase), h.Name)
		if st, err := ReadState(statePath); err == nil && st != nil {
			r.log.Warn("skip",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.String("reason", SkipRunOnceDone),
				log.String("state_file", statePath),
				log.Int("runs", st.Runs),
			)
			res.Skipped, res.SkipReason = true, SkipRunOnceDone
			return res
		}
	}
	switch h.Target {
	case hook.TargetMaster:
		if !r.opt.Env.IsMaster && r.opt.Env.MasterKnown {
			return r.skip(h, res, SkipNotMaster, log.Info)
		}
		if !r.opt.Env.MasterKnown {
			// Pre-quorate: sballd is down, so the master id cannot be read.
			// Running is the safer default and is logged loudly.
			r.log.Warn("target_unknown",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.String("target", string(h.Target)),
				log.String("reason", "master-unknown-before-quorate"),
			)
		}
	case hook.TargetAll:
		// MVP: no cross-node coordination, every node runs its own copy.
		r.log.Debug("target_all",
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
		)
	}

	// --- dry run ----------------------------------------------------------
	if r.opt.DryRun {
		r.log.Info("dry_run",
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.String("host", r.opt.Env.Hostname),
			log.String("target", string(h.Target)),
			log.Int("timeout", timeout),
			log.Int("max_attempts", h.Retry+1),
			log.Int("backoff_seconds", h.RetryBackoff),
			log.String("on_fail", h.EffectiveOnFail(r.opt.PhaseOnFailure)),
			log.String("file", h.Path),
		)
		res.Skipped, res.SkipReason = true, SkipDryRun
		res.Duration = r.now().Sub(res.Started)
		return res
	}

	// --- execution --------------------------------------------------------
	backoff := time.Duration(h.RetryBackoff) * time.Second
	for attempt := 1; attempt <= h.Retry+1; attempt++ {
		res.Attempts = attempt
		hookEnv := r.hookEnv(h, attempt, timeout)

		r.log.Info("start",
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.String("host", r.opt.Env.Hostname),
			log.String("target", string(h.Target)),
			log.Bool("is_master", r.opt.Env.IsMaster),
			log.Int("attempt", attempt),
			log.Int("timeout", timeout),
			log.String("run_id", r.runID),
		)

		started := r.now()
		out := r.exec(ctx, h, hookEnv, timeout)
		res.Duration = r.now().Sub(started)
		res.ExitCode = out.exitCode
		res.TimedOut = out.timedOut
		res.Stdout = out.stdout
		res.Stderr = out.stderr
		res.Err = out.err

		fields := []log.Field{
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.Int("exit", res.ExitCode),
			log.Millis("duration_ms", res.Duration),
			log.Int("attempt", attempt),
			log.String("run_id", r.runID),
		}

		switch {
		case out.timedOut:
			fields = append(fields,
				log.Int("timeout_seconds", timeout),
				log.String("stderr", tailText(out.stderr, 2000)))
			r.log.Error("timeout", fields...)
		case out.err != nil:
			fields = append(fields,
				log.String("error", errText(out.err)),
				log.String("stderr", tailText(out.stderr, 2000)))
			r.log.Error("fail", fields...)
		default:
			if out.stdout != "" {
				r.log.Debug("stdout",
					log.String("phase", string(h.Phase)),
					log.String("hook", h.Name),
					log.String("output", tailText(out.stdout, 2000)))
			}
			r.log.Info("finish", fields...)
		}

		if out.err == nil {
			break
		}
		if attempt <= h.Retry {
			r.log.Warn("retry",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.Int("attempt", attempt),
				log.Int("retry", attempt),
				log.Int("max_attempts", h.Retry+1),
				log.Int("backoff_seconds", h.RetryBackoff),
				log.Int("exit", res.ExitCode),
				log.String("stderr", tailText(out.stderr, 500)),
			)
			if backoff > 0 {
				select {
				case <-time.After(backoff):
				case <-ctx.Done():
					res.Err = ctx.Err()
					r.recordLastRun(res)
					return res
				}
			}
			continue
		}
		r.log.Error("give_up",
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.Int("attempts", attempt),
			log.Int("exit", res.ExitCode),
		)
	}

	// --- RUN_ONCE bookkeeping --------------------------------------------
	if res.Err == nil && h.RunOnce && !r.opt.Force {
		statePath := r.opt.Paths.StateFile(string(h.Phase), h.Name)
		prev, _ := ReadState(statePath)
		st := &HookState{
			Hook:     h.Name,
			Phase:    string(h.Phase),
			Host:     r.opt.Env.Hostname,
			FirstRun: r.now(),
			LastRun:  r.now(),
			Runs:     1,
			LastExit: res.ExitCode,
		}
		if prev != nil {
			st = prev
			st.LastRun = r.now()
			st.Runs++
			st.LastExit = res.ExitCode
			if st.FirstRun.IsZero() {
				st.FirstRun = r.now()
			}
		}
		if err := WriteState(statePath, st); err != nil {
			r.log.Warn("state_write_failed",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.String("state_file", statePath),
				log.String("error", err.Error()),
			)
		} else {
			r.log.Debug("state_written",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.String("state_file", statePath),
			)
		}
	}

	r.recordLastRun(res)
	return res
}

func (r *Runner) skip(h *hook.Meta, res *Result, reason string, level log.Level) *Result {
	r.log.Log(level, "skip",
		log.String("phase", string(h.Phase)),
		log.String("hook", h.Name),
		log.String("reason", reason),
	)
	res.Skipped, res.SkipReason = true, reason
	res.Duration = r.now().Sub(res.Started)
	return res
}

func (r *Runner) recordLastRun(res *Result) {
	if r.opt.SkipLastRun || res.Skipped {
		return
	}
	event := "finish"
	switch {
	case res.TimedOut:
		event = "timeout"
	case res.Err != nil:
		event = "fail"
	}
	rec := LastRun{
		Hook:       res.Hook.Name,
		Phase:      string(res.Hook.Phase),
		Host:       r.opt.Env.Hostname,
		Event:      event,
		Exit:       res.ExitCode,
		Attempts:   res.Attempts,
		DurationMS: res.Duration.Milliseconds(),
		TimedOut:   res.TimedOut,
		At:         r.now(),
	}
	if err := UpdateLastRun(r.opt.LastRunFile, rec); err != nil {
		r.log.Warn("last_run_write_failed",
			log.String("phase", string(res.Hook.Phase)),
			log.String("hook", res.Hook.Name),
			log.String("error", err.Error()),
		)
	}
}

// execResult is the raw outcome of one process execution.
type execResult struct {
	exitCode int
	timedOut bool
	stdout   string
	stderr   string
	err      error
}

// terminationGrace is how long a hook may take to die after SIGTERM before it
// is SIGKILLed.
const terminationGrace = 5 * time.Second

func (r *Runner) exec(ctx context.Context, h *hook.Meta, env []string, timeout int) execResult {
	cmd := exec.Command(h.Path)
	cmd.Dir = "/"
	cmd.Env = env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	outBuf := newTailBuffer(maxCapture)
	errBuf := newTailBuffer(maxCapture)
	stdout, stderr := io.Writer(outBuf), io.Writer(errBuf)
	if r.opt.Stream {
		stdout = io.MultiWriter(outBuf, r.opt.Stdout)
		stderr = io.MultiWriter(errBuf, r.opt.Stderr)
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return execResult{exitCode: -1, err: fmt.Errorf("start %s: %w", h.Path, err)}
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	var timeoutC <-chan time.Time
	if timeout > 0 {
		t := time.NewTimer(time.Duration(timeout) * time.Second)
		defer t.Stop()
		timeoutC = t.C
	}

	wait := func(res execResult) execResult {
		res.stdout = outBuf.String()
		res.stderr = errBuf.String()
		return res
	}

	select {
	case err := <-done:
		return wait(execResult{exitCode: exitCodeOf(err), err: err})
	case <-timeoutC:
		killGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(terminationGrace):
			killGroup(cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return wait(execResult{
			exitCode: 128 + int(syscall.SIGTERM),
			timedOut: true,
			err:      fmt.Errorf("timed out after %ds", timeout),
		})
	case <-ctx.Done():
		killGroup(cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(terminationGrace):
			killGroup(cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return wait(execResult{exitCode: 130, err: ctx.Err()})
	}
}

func killGroup(pid int, sig syscall.Signal) {
	if pid <= 0 {
		return
	}
	// Negative pid targets the whole process group, so children spawned by the
	// hook die with it.
	if err := syscall.Kill(-pid, sig); err != nil {
		_ = syscall.Kill(pid, sig)
	}
}

func exitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
			if status.Signaled() {
				return 128 + int(status.Signal())
			}
			return status.ExitStatus()
		}
		return exitErr.ExitCode()
	}
	return -1
}

// hookEnv builds the environment for one attempt.
func (r *Runner) hookEnv(h *hook.Meta, attempt, timeout int) []string {
	env := make([]string, 0, len(os.Environ())+16)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "SBALL_") {
			continue // superseded by the values below
		}
		env = append(env, kv)
	}
	yesNo := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	env = append(env,
		EnvPhase+"="+string(h.Phase),
		EnvName+"="+h.Name,
		EnvHost+"="+r.opt.Env.Hostname,
		EnvMasterID+"="+r.opt.Env.MasterID,
		EnvIsMaster+"="+yesNo(r.opt.Env.IsMaster),
		EnvQuorate+"="+r.opt.Env.QuorateState,
		EnvLogFile+"="+r.opt.Paths.LogFile,
		EnvHookPath+"="+h.Path,
		EnvHookDir+"="+r.opt.Paths.Dir(string(h.Phase)),
		EnvStateDir+"="+r.opt.Paths.PhaseStateDir(string(h.Phase)),
		EnvStateFile+"="+r.opt.Paths.StateFile(string(h.Phase), h.Name),
		EnvRuntime+"="+r.opt.Paths.RuntimeDir,
		EnvCommonLib+"="+filepath.Join(r.opt.Paths.LibDir, "common.sh"),
		EnvRunID+"="+r.runID,
		EnvAttempt+"="+fmt.Sprintf("%d", attempt),
		EnvRetries+"="+fmt.Sprintf("%d", h.Retry),
		EnvVersion+"="+r.opt.Env.Version,
	)
	return env
}

// maxCapture bounds the per-stream output kept for one hook run.
const maxCapture = 64 * 1024

// tailBuffer keeps the last max bytes written to it, so a chatty hook cannot
// exhaust memory while its output is still available for the log.
type tailBuffer struct {
	max int
	buf []byte
}

func newTailBuffer(max int) *tailBuffer { return &tailBuffer{max: max} }

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	if len(b.buf)+len(p) > b.max {
		drop := len(b.buf) + len(p) - b.max
		b.buf = append(b.buf[:0], b.buf[drop:]...)
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

func (b *tailBuffer) String() string { return string(b.buf) }

// tailText returns at most n bytes from the end of s, with control characters
// removed so log records stay single line friendly.
func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) > n {
		s = "..." + s[len(s)-n:]
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			sb.WriteRune(' ')
		case r == '\r':
		case r < 0x20:
		default:
			sb.WriteRune(r)
		}
	}
	return strings.TrimSpace(sb.String())
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return tailText(err.Error(), 500)
}
