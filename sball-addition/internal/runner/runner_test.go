package runner

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/log"
)

// harness bundles the temporary installation used by the tests.
type harness struct {
	t     *testing.T
	cfg   *config.Config
	paths config.Paths
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	for _, dir := range []string{cfg.Paths.PreDir, cfg.Paths.PostDir, cfg.Paths.StateDir, cfg.Paths.RuntimeDir, cfg.Paths.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &harness{t: t, cfg: cfg, paths: cfg.Paths}
}

// writeHook creates a hook script and returns its parsed metadata.
func (h *harness) writeHook(phase hook.Phase, fileName, name, extraHeader, body string) *hook.Meta {
	h.t.Helper()
	header := "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n" +
		"# HOOK: " + string(phase) + "\n# NAME: " + name + "\n" + extraHeader +
		"### END SBALL-ADDITION INFO\n"
	content := header + body
	dir := h.paths.Dir(string(phase))
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		h.t.Fatal(err)
	}
	set := hook.LoadPhase(dir, phase)
	if len(set.Errors) > 0 {
		h.t.Fatalf("hook set errors: %v", set.Errors)
	}
	m := set.Find(name)
	if m == nil {
		h.t.Fatalf("hook %q not found in %s", name, dir)
	}
	return m
}

func (h *harness) runner(phase hook.Phase, env Env, mutate func(*Options)) *Runner {
	h.t.Helper()
	if env.Hostname == "" {
		env.Hostname = "testhost"
	}
	if env.Version == "" {
		env.Version = "test"
	}
	opt := Options{
		Phase:          phase,
		Paths:          h.paths,
		DefaultTimeout: 5,
		PhaseOnFailure: hook.OnFailContinue,
		Env:            env,
		Logger:         log.NewStderr(io.Discard, log.Debug),
	}
	if mutate != nil {
		mutate(&opt)
	}
	return New(opt)
}

func TestRunSuccess(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(h.paths.RuntimeDir, "ok.txt")
	m := h.writeHook(hook.PhasePost, "10-demo.sh", "demo", "", "echo hello > "+out+"\necho to-stdout\n")
	r := h.runner(hook.PhasePost, Env{}, nil)

	res := r.RunOne(context.Background(), m)
	if res.Err != nil {
		t.Fatalf("unexpected failure: %v (stderr=%s)", res.Err, res.Stderr)
	}
	if res.ExitCode != 0 || res.Skipped {
		t.Errorf("res = %+v", res)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("the hook body did not run: %v", err)
	}
	if !strings.Contains(res.Stdout, "to-stdout") {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.Attempts != 1 {
		t.Errorf("attempts = %d", res.Attempts)
	}
}

func TestRunFailure(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-fail.sh", "fail", "", "echo boom >&2\nexit 3\n")
	sum := h.runner(hook.PhasePost, Env{}, nil).Run(context.Background(), []*hook.Meta{m})

	if len(sum.Failures()) != 1 {
		t.Fatalf("failures = %d, want 1", len(sum.Failures()))
	}
	res := sum.Results[0]
	if res.ExitCode != 3 {
		t.Errorf("exit = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(res.Stderr, "boom") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if sum.Aborted {
		t.Error("ON_FAIL=continue must not abort the phase")
	}
	if sum.Err() != nil {
		t.Errorf("Err() = %v, want nil with continue", sum.Err())
	}
}

func TestRunAbortsOnFailAbort(t *testing.T) {
	h := newHarness(t)
	marker := filepath.Join(h.paths.RuntimeDir, "second-ran")
	first := h.writeHook(hook.PhasePre, "10-first.sh", "first", "# ON_FAIL: abort\n", "exit 1\n")
	second := h.writeHook(hook.PhasePre, "20-second.sh", "second", "", "touch "+marker+"\n")
	r := h.runner(hook.PhasePre, Env{}, func(o *Options) { o.PhaseOnFailure = hook.OnFailContinue })

	sum := r.Run(context.Background(), []*hook.Meta{first, second})
	if !sum.Aborted {
		t.Fatal("summary should be aborted")
	}
	if sum.AbortHook != "first" {
		t.Errorf("abort hook = %q", sum.AbortHook)
	}
	if len(sum.Results) != 1 {
		t.Errorf("the second hook must not run, results = %d", len(sum.Results))
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("the second hook ran after an abort")
	}
	if sum.Err() == nil {
		t.Error("Err() must be non-nil after an abort")
	}
}

func TestRetryWithBackoff(t *testing.T) {
	h := newHarness(t)
	counter := filepath.Join(h.paths.RuntimeDir, "counter")
	body := "n=$(cat " + counter + " 2>/dev/null || echo 0)\n" +
		"n=$((n+1))\n" +
		"echo $n > " + counter + "\n" +
		"if [ \"$n\" -lt 3 ]; then echo attempt $n failed >&2; exit 1; fi\n" +
		"exit 0\n"
	m := h.writeHook(hook.PhasePost, "10-flaky.sh", "flaky", "# RETRY: 2\n# RETRY_BACKOFF: 1\n", body)
	r := h.runner(hook.PhasePost, Env{}, nil)

	start := time.Now()
	res := r.RunOne(context.Background(), m)
	if res.Err != nil {
		t.Fatalf("hook should succeed on the third attempt: %v (%s)", res.Err, res.Stderr)
	}
	if res.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", res.Attempts)
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Errorf("backoff was not applied: %s", elapsed)
	}
}

func TestRetryGivesUp(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-always.sh", "always", "# RETRY: 1\n# RETRY_BACKOFF: 0\n", "exit 7\n")
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m)
	if res.Err == nil {
		t.Fatal("hook should fail")
	}
	if res.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 (RETRY=1)", res.Attempts)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit = %d, want 7", res.ExitCode)
	}
}

func TestTimeout(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-slow.sh", "slow", "# TIMEOUT: 1\n", "sleep 30\n")
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m)

	if !res.TimedOut {
		t.Fatalf("hook should have timed out: %+v", res)
	}
	if res.Err == nil {
		t.Error("a timeout is a failure")
	}
	if res.Duration > 3*time.Second {
		t.Errorf("timeout took %s, the SIGTERM grace period seems to be missing", res.Duration)
	}
	if res.ExitCode != 143 {
		t.Errorf("exit = %d, want 143 (128+SIGTERM)", res.ExitCode)
	}
}

func TestRunOnce(t *testing.T) {
	h := newHarness(t)
	counter := filepath.Join(h.paths.RuntimeDir, "runs")
	m := h.writeHook(hook.PhasePost, "10-once.sh", "once", "# RUN_ONCE: true\n",
		"echo x >> "+counter+"\n")
	r := h.runner(hook.PhasePost, Env{}, nil)

	if res := r.RunOne(context.Background(), m); res.Err != nil || res.Skipped {
		t.Fatalf("first run: %+v", res)
	}
	statePath := h.paths.StateFile("post-quorate", "once")
	st, err := ReadState(statePath)
	if err != nil {
		t.Fatalf("state file: %v", err)
	}
	if st == nil || st.Runs != 1 {
		t.Fatalf("state = %+v", st)
	}

	res := r.RunOne(context.Background(), m)
	if !res.Skipped || res.SkipReason != SkipRunOnceDone {
		t.Fatalf("second run should be skipped: %+v", res)
	}

	forced := h.runner(hook.PhasePost, Env{}, func(o *Options) { o.Force = true })
	if res := forced.RunOne(context.Background(), m); res.Skipped {
		t.Errorf("--force must run the hook anyway: %+v", res)
	}

	data, _ := os.ReadFile(counter)
	if lines := strings.Count(string(data), "x"); lines != 2 {
		t.Errorf("hook ran %d times, want 2 (skip + force)", lines)
	}
}

func TestDisabledHookIsSkipped(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-off.sh", "off", "# ENABLED: false\n", "exit 1\n")
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m)
	if !res.Skipped || res.SkipReason != SkipDisabled {
		t.Fatalf("res = %+v", res)
	}
}

func TestNonExecutableHookIsSkipped(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-noexec.sh", "noexec", "", "exit 0\n")
	if err := os.Chmod(m.Path, 0o644); err != nil {
		t.Fatal(err)
	}
	set := hook.LoadPhase(h.paths.PostDir, hook.PhasePost)
	m = set.Find("noexec")
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m)
	if !res.Skipped || res.SkipReason != SkipNotExec {
		t.Fatalf("res = %+v", res)
	}
}

func TestTargetMasterSkipsNonMaster(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-master.sh", "master-only", "# TARGET: master\n", "exit 0\n")
	env := Env{MasterID: "1", MasterKnown: true, IsMaster: false}
	res := h.runner(hook.PhasePost, env, nil).RunOne(context.Background(), m)
	if !res.Skipped || res.SkipReason != SkipNotMaster {
		t.Fatalf("res = %+v", res)
	}
}

func TestTargetMasterRunsOnMaster(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-master.sh", "master-only", "# TARGET: master\n", "exit 0\n")
	env := Env{MasterID: "1", MasterKnown: true, IsMaster: true}
	res := h.runner(hook.PhasePost, env, nil).RunOne(context.Background(), m)
	if res.Skipped || res.Err != nil {
		t.Fatalf("res = %+v", res)
	}
}

func TestTargetMasterRunsWhenMasterUnknown(t *testing.T) {
	// pre-quorate: sballd is down, the master id cannot be read, so the hook
	// runs and the runner logs a warning.
	h := newHarness(t)
	m := h.writeHook(hook.PhasePre, "10-master.sh", "master-only", "# TARGET: master\n", "exit 0\n")
	env := Env{MasterKnown: false, IsMaster: false, QuorateState: "down"}
	res := h.runner(hook.PhasePre, env, nil).RunOne(context.Background(), m)
	if res.Skipped {
		t.Fatalf("hook should run while the master is unknown: %+v", res)
	}
	if res.Err != nil {
		t.Fatalf("hook failed: %v", res.Err)
	}
}

func TestEnvironmentVariables(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(h.paths.RuntimeDir, "env.txt")
	body := "env | grep '^SBALL_' | sort > " + out + "\n"
	m := h.writeHook(hook.PhasePost, "10-env.sh", "env", "", body)
	env := Env{
		Hostname:     "spongebob",
		MasterID:     "1",
		MasterKnown:  true,
		IsMaster:     true,
		QuorateState: "quorate",
		Version:      "9.9.9",
	}
	res := h.runner(hook.PhasePost, env, nil).RunOne(context.Background(), m)
	if res.Err != nil {
		t.Fatalf("hook failed: %v (%s)", res.Err, res.Stderr)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	want := map[string]string{
		"SBALL_HOOK_PHASE":       "post-quorate",
		"SBALL_HOOK_NAME":        "env",
		"SBALL_HOSTNAME":         "spongebob",
		"SBALL_MASTER_ID":        "1",
		"SBALL_IS_MASTER":        "true",
		"SBALL_QUORATE_STATE":    "quorate",
		"SBALL_LOG_FILE":         h.paths.LogFile,
		"SBALL_STATE_FILE":       h.paths.StateFile("post-quorate", "env"),
		"SBALL_ADDITION_VERSION": "9.9.9",
		"SBALL_RETRY_ATTEMPT":    "1",
	}
	for key, value := range want {
		if !strings.Contains(got, key+"="+value) {
			t.Errorf("environment misses %s=%s\n%s", key, value, got)
		}
	}
	if !strings.Contains(got, "SBALL_RUN_ID=") {
		t.Error("SBALL_RUN_ID is missing")
	}
}

func TestLastRunRegistry(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-record.sh", "record", "", "exit 0\n")
	r := h.runner(hook.PhasePost, Env{Hostname: "node1"}, nil)
	if res := r.RunOne(context.Background(), m); res.Err != nil {
		t.Fatal(res.Err)
	}
	runs, err := ReadLastRuns(h.paths.LastRunFile())
	if err != nil {
		t.Fatal(err)
	}
	rec, ok := runs[LastRunKey("post-quorate", "record")]
	if !ok {
		t.Fatalf("last run registry = %+v", runs)
	}
	if rec.Event != "finish" || rec.Exit != 0 || rec.Host != "node1" {
		t.Errorf("record = %+v", rec)
	}
	if rec.At.IsZero() {
		t.Error("timestamp missing")
	}
}

func TestSkippedHooksAreNotRecorded(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-off.sh", "off", "# ENABLED: false\n", "exit 0\n")
	if res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m); !res.Skipped {
		t.Fatalf("res = %+v", res)
	}
	runs, _ := ReadLastRuns(h.paths.LastRunFile())
	if len(runs) != 0 {
		t.Errorf("skipped hooks must not be recorded: %+v", runs)
	}
}

func TestProgrammaticIntentionalFailure(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-kill.sh", "kill", "", "kill -TERM $$\n")
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(context.Background(), m)
	if res.Err == nil {
		t.Fatal("a self-killed hook must be a failure")
	}
	if res.ExitCode != 143 {
		t.Errorf("exit = %d, want 143", res.ExitCode)
	}
}

func TestContextCancellation(t *testing.T) {
	h := newHarness(t)
	m := h.writeHook(hook.PhasePost, "10-wait.sh", "wait", "# TIMEOUT: 30\n", "sleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	res := h.runner(hook.PhasePost, Env{}, nil).RunOne(ctx, m)
	if res.Err == nil {
		t.Fatal("cancellation must fail the hook")
	}
	if res.TimedOut {
		t.Error("a cancelled run is not a timeout")
	}
}

func TestTailBufferKeepsTheEnd(t *testing.T) {
	b := newTailBuffer(16)
	if _, err := b.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "0123456789abcdef" {
		t.Fatalf("got %q", got)
	}
	if _, err := b.Write([]byte("XYZ")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "3456789abcdefXYZ" {
		t.Fatalf("got %q", got)
	}
	if _, err := b.Write([]byte("0123456789ABCDEFGHIJ")); err != nil {
		t.Fatal(err)
	}
	if got := b.String(); got != "456789ABCDEFGHIJ" {
		t.Fatalf("got %q (the last 16 bytes)", got)
	}
}

func TestTailTextIsSingleLine(t *testing.T) {
	got := tailText("line1\nline2\r\nline3", 100)
	if strings.ContainsAny(got, "\n\r") {
		t.Errorf("got %q, want a single line", got)
	}
	if !strings.HasSuffix(got, "line3") {
		t.Errorf("got %q, want the end of the output", got)
	}
}
