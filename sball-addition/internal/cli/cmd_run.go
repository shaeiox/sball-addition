package cli

import (
	"fmt"
	"strings"
	"time"

	"sball-addition/internal/hook"
	"sball-addition/internal/log"
	"sball-addition/internal/runner"
	"sball-addition/internal/version"
)

func (a *App) cmdRun(args []string) int {
	f := newFlagSet("run", args)
	phaseStr := f.String("phase", "", "phase of the hook (disambiguates a duplicate name)")
	force := f.Bool("force", false, "ignore the RUN_ONCE state file")
	noState := f.Bool("no-state", false, "do not touch state/runtime files")
	dryRun := f.Bool("dry-run", false, "show what would run without executing")
	quiet := f.Bool("quiet", false, "do not mirror the hook output")
	if err := f.Parse(); err != nil {
		return a.usageError("run", err)
	}
	name := f.Arg(0)
	if name == "" {
		return a.usageError("run", fmt.Errorf("missing <hook-name>"))
	}
	phases, err := selectPhases(*phaseStr)
	if err != nil {
		return a.usageError("run", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}

	var found *hook.Meta
	for _, phase := range phases {
		set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
		for _, w := range set.Warnings {
			fmt.Fprintf(a.Stderr, "%s: warning: %s\n", version.Name, w)
		}
		if h := set.Find(name); h != nil {
			found = h
			break
		}
	}
	if found == nil {
		fmt.Fprintf(a.Stderr, "%s: hook %q not found in %s\n", version.Name, name, strings.Join(phaseNames(phases), ", "))
		return ExitNotFound
	}

	ctx, cancel := signalContext()
	defer cancel()

	r := runner.New(runner.Options{
		Phase:          found.Phase,
		Paths:          cfg.Paths,
		DefaultTimeout: cfg.General.DefaultTimeout,
		PhaseOnFailure: a.phasePolicy(cfg, found.Phase),
		Force:          *force,
		DryRun:         *dryRun,
		Env:            a.clusterEnv(cfg, lg),
		Logger:         lg,
		Stream:         !*quiet,
		Stdout:         a.Stdout,
		Stderr:         a.Stderr,
		SkipLastRun:    *noState,
	})

	res := r.RunOne(ctx, found)
	a.printResult(res)
	lg.Info("manual_run",
		log.String("phase", string(found.Phase)),
		log.String("hook", found.Name),
		log.Int("exit", res.ExitCode),
		log.Bool("skipped", res.Skipped),
		log.Millis("duration_ms", res.Duration),
	)
	return exitFromResult(res)
}

func (a *App) cmdRunPhase(args []string) int {
	f := newFlagSet("run-phase", args)
	phaseStr := f.String("phase", "", "phase to run")
	force := f.Bool("force", false, "ignore RUN_ONCE state files")
	quiet := f.Bool("quiet", false, "do not mirror hook output")
	asJSON := f.Bool("json", false, "print a machine readable summary")
	dryRun := f.Bool("dry-run", false, "show what would run without executing")
	if err := f.Parse(); err != nil {
		return a.usageError("run-phase", err)
	}
	raw := *phaseStr
	if raw == "" {
		raw = f.Arg(0)
	}
	if raw == "" {
		return a.usageError("run-phase", fmt.Errorf("missing <phase>"))
	}
	phase, err := hook.ParsePhase(raw)
	if err != nil {
		return a.usageError("run-phase", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}

	set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
	if !set.Runnable() {
		for _, e := range set.Errors {
			lg.Error("hook_error", log.String("phase", string(phase)), log.String("error", e.Error()))
			fmt.Fprintf(a.Stderr, "%s: %s\n", version.Name, e.Error())
		}
		return ExitNotFound
	}
	for _, w := range set.Warnings {
		lg.Warn("hook_warning", log.String("phase", string(phase)), log.String("warning", w))
	}

	if !a.phaseEnabled(cfg, phase) {
		lg.Warn("phase_disabled", log.String("phase", string(phase)))
		fmt.Fprintf(a.Stdout, "%s: disabled in %s\n", phase, cfg.Paths.ConfigFile)
		return ExitOK
	}

	hooks := set.Hooks
	if len(hooks) == 0 {
		lg.Info("phase_empty", log.String("phase", string(phase)), log.String("dir", set.Dir))
		if phase == hook.PhasePre && !cfg.PreQuorate.ContinueOnEmpty {
			fmt.Fprintf(a.Stderr, "%s: no pre-quorate hooks found in %s and continue_on_empty=false\n", version.Name, set.Dir)
			return ExitFailure
		}
		fmt.Fprintf(a.Stdout, "%s: no hooks installed in %s\n", phase, set.Dir)
		return ExitOK
	}

	ctx, cancel := signalContext()
	defer cancel()

	r := runner.New(runner.Options{
		Phase:          phase,
		Paths:          cfg.Paths,
		DefaultTimeout: cfg.General.DefaultTimeout,
		PhaseOnFailure: a.phasePolicy(cfg, phase),
		Force:          *force,
		DryRun:         *dryRun,
		Env:            a.clusterEnv(cfg, lg),
		Logger:         lg,
		Stream:         !*quiet,
		Stdout:         a.Stdout,
		Stderr:         a.Stderr,
	})
	sum := r.Run(ctx, hooks)

	if *asJSON {
		a.printJSON(summaryJSON(sum))
	} else {
		for _, res := range sum.Results {
			if res.Skipped && res.SkipReason != runner.SkipDryRun {
				continue
			}
			a.printResult(res)
		}
		fmt.Fprintf(a.Stdout, "%s: %d ran, %d ok, %d failed, %d skipped in %s\n",
			phase, sum.Ran(), sum.OK(), len(sum.Failures()), sum.Skipped(), fmtDuration(sum.Duration))
		if len(sum.Failures()) > 0 && !sum.Aborted {
			fmt.Fprintf(a.Stderr, "%s: %d hook(s) failed but ON_FAIL=continue, continuing\n",
				version.Name, len(sum.Failures()))
		}
	}
	if err := sum.Err(); err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
	return ExitOK
}

// printResult prints one line per hook run, unless the output was streamed
// live and already seen by the operator.
func (a *App) printResult(res *runner.Result) {
	name := string(res.Hook.Phase) + "/" + res.Hook.Name
	switch {
	case res.SkipReason == runner.SkipDryRun:
		fmt.Fprintf(a.Stdout, "%s: would run (timeout %ds, up to %d attempt(s), ON_FAIL=%s)\n",
			name, res.Hook.EffectiveTimeout(a.cfg.General.DefaultTimeout), res.Hook.Retry+1,
			res.Hook.EffectiveOnFail(a.phasePolicy(a.cfg, res.Hook.Phase)))
	case res.Skipped:
		fmt.Fprintf(a.Stdout, "%s: SKIP (%s)\n", name, res.SkipReason)
	case res.Err == nil:
		fmt.Fprintf(a.Stdout, "%s: OK (exit %d, %s, %d attempt(s))\n",
			name, res.ExitCode, fmtDuration(res.Duration), res.Attempts)
	default:
		detail := ""
		if res.TimedOut {
			detail = ", timed out"
		}
		if s := lastLine(res.Stderr); s != "" {
			detail += ": " + s
		}
		fmt.Fprintf(a.Stderr, "%s: FAIL (exit %d, %s, %d attempt(s)%s)\n",
			name, res.ExitCode, fmtDuration(res.Duration), res.Attempts, detail)
	}
}

func exitFromResult(res *runner.Result) int {
	if res.Skipped || res.Err == nil {
		return ExitOK
	}
	if res.ExitCode > 0 && res.ExitCode < 256 {
		return res.ExitCode
	}
	return ExitFailure
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if len(last) > 300 {
		last = last[:300] + "..."
	}
	return last
}

func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.2fs", d.Seconds())
	default:
		return d.Round(time.Second).String()
	}
}

type summaryJSONHook struct {
	Hook       string `json:"hook"`
	Phase      string `json:"phase"`
	Skipped    bool   `json:"skipped"`
	SkipReason string `json:"skip_reason,omitempty"`
	Exit       int    `json:"exit"`
	Attempts   int    `json:"attempts"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Error      string `json:"error,omitempty"`
}

func summaryJSON(sum *runner.Summary) any {
	hooks := make([]summaryJSONHook, 0, len(sum.Results))
	for _, r := range sum.Results {
		h := summaryJSONHook{
			Hook:       r.Hook.Name,
			Phase:      string(r.Hook.Phase),
			Skipped:    r.Skipped,
			SkipReason: r.SkipReason,
			Exit:       r.ExitCode,
			Attempts:   r.Attempts,
			DurationMS: r.Duration.Milliseconds(),
			TimedOut:   r.TimedOut,
		}
		if r.Err != nil {
			h.Error = r.Err.Error()
		}
		hooks = append(hooks, h)
	}
	return struct {
		Phase      string            `json:"phase"`
		RunID      string            `json:"run_id"`
		Ran        int               `json:"ran"`
		OK         int               `json:"ok"`
		Failed     int               `json:"failed"`
		Skipped    int               `json:"skipped"`
		Aborted    bool              `json:"aborted"`
		DurationMS int64             `json:"duration_ms"`
		Hooks      []summaryJSONHook `json:"hooks"`
	}{
		Phase:      string(sum.Phase),
		RunID:      sum.RunID,
		Ran:        sum.Ran(),
		OK:         sum.OK(),
		Failed:     len(sum.Failures()),
		Skipped:    sum.Skipped(),
		Aborted:    sum.Aborted,
		DurationMS: sum.Duration.Milliseconds(),
		Hooks:      hooks,
	}
}

func phaseNames(phases []hook.Phase) []string {
	out := make([]string, 0, len(phases))
	for _, p := range phases {
		out = append(out, string(p))
	}
	return out
}
