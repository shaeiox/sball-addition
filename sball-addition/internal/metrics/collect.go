package metrics

import (
	"sort"
	"time"
)

// window sizes used for the counter metrics derived from the log file.
var logWindows = []struct {
	label string
	dur   time.Duration
}{
	{"1h", time.Hour},
	{"24h", 24 * time.Hour},
	{"7d", 7 * 24 * time.Hour},
}

// Collect turns a snapshot of the node state into Prometheus metrics.
func Collect(d *Deps) []*Metric {
	now := d.Now
	if now.IsZero() {
		now = time.Now()
	}

	info := &Metric{
		Name: "sball_addition_info",
		Help: "Build and identity information of the sball-addition installation.",
		Type: "gauge",
	}
	info.Add(1,
		L("version", orUnknown(d.Version)),
		L("host", orUnknown(d.Host)),
		L("config", orUnknown(d.ConfigFile)),
	)

	// ---------------------------------------------------------------- node ----
	sballd := &Metric{
		Name: "sball_addition_sballd_up",
		Help: "1 when an sballd process is running on this node.",
		Type: "gauge",
	}
	sballd.Add(boolValue(d.SballdPID > 0))

	socket := &Metric{
		Name: "sball_addition_pvm_socket_reachable",
		Help: "1 when the PVM XML-RPC socket answered the status query.",
		Type: "gauge",
	}
	socket.Add(boolValue(d.SocketOK))

	quorate := &Metric{
		Name: "sball_addition_quorate",
		Help: "1 when this node reported the quorate state (sball-state) in the last query.",
		Type: "gauge",
	}
	quorate.Add(boolValue(d.Quorate))

	master := &Metric{
		Name: "sball_addition_is_master",
		Help: "1 when this node is the cluster master according to sballMaster().",
		Type: "gauge",
	}
	master.Add(boolValue(d.IsMaster))

	watcher := &Metric{
		Name: "sball_addition_watcher_running",
		Help: "1 when a post-quorate watcher process is alive on this node.",
		Type: "gauge",
	}
	watcher.Add(boolValue(d.WatcherPID > 0))

	// --------------------------------------------------------------- hooks ----
	phaseHooks := &Metric{
		Name: "sball_addition_hooks",
		Help: "Number of hooks per phase and their enabled state.",
		Type: "gauge",
	}
	for _, p := range []struct {
		name string
		info PhaseInfo
	}{{"pre-quorate", d.Pre}, {"post-quorate", d.Post}} {
		phaseHooks.Add(float64(p.info.Hooks), L("phase", p.name), L("state", "installed"))
		phaseHooks.Add(float64(p.info.On), L("phase", p.name), L("state", "enabled"))
		phaseHooks.Add(boolValue(p.info.Enabled), L("phase", p.name), L("state", "phase_enabled"))
	}

	lastSuccess := &Metric{
		Name: "sball_addition_hook_last_success_timestamp_seconds",
		Help: "Unix timestamp of the last successful run of a hook (0 when it never succeeded).",
		Type: "gauge",
	}
	lastExit := &Metric{
		Name: "sball_addition_hook_last_exit_code",
		Help: "Exit code of the most recent run of a hook.",
		Type: "gauge",
	}
	lastDuration := &Metric{
		Name: "sball_addition_hook_last_duration_seconds",
		Help: "Duration of the most recent run of a hook.",
		Type: "gauge",
	}
	lastSkipped := &Metric{
		Name: "sball_addition_hook_last_run_timed_out",
		Help: "1 when the most recent run of a hook was killed because it exceeded TIMEOUT.",
		Type: "gauge",
	}

	keys := make([]string, 0, len(d.LastRuns))
	for k, rec := range d.LastRuns {
		if rec.Phase == "" || rec.Hook == "" {
			_ = k
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic output

	seenSuccess := map[string]bool{}
	for _, k := range keys {
		rec := d.LastRuns[k]
		labels := []Label{L("phase", rec.Phase), L("hook", rec.Hook)}
		lastExit.Add(float64(rec.Exit), labels...)
		lastDuration.Add(float64(rec.DurationMS)/1000.0, labels...)
		lastSkipped.Add(boolValue(rec.TimedOut), labels...)
		if rec.Event == "finish" && !rec.TimedOut {
			if !rec.At.IsZero() {
				lastSuccess.Add(float64(rec.At.Unix()), labels...)
				seenSuccess[k] = true
			}
		}
		if !seenSuccess[k] {
			lastSuccess.Add(0, labels...)
		}
	}

	// ------------------------------------------------ log derived counters ----
	events := ParseLogLines(d.LogLines)

	failures := &Metric{
		Name: "sball_addition_hook_failures_total",
		Help: "Hook failures (event=fail|timeout|give_up) per phase/hook inside the window.",
		Type: "counter",
	}
	retries := &Metric{
		Name: "sball_addition_hook_retries_total",
		Help: "Retry attempts per phase/hook inside the window.",
		Type: "counter",
	}
	pollTimeouts := &Metric{
		Name: "sball_addition_poll_timeouts_total",
		Help: "Times the watcher gave up waiting for the quorate state, per window.",
		Type: "counter",
	}
	phaseAborts := &Metric{
		Name: "sball_addition_phase_aborts_total",
		Help: "Times a phase was aborted because a hook with ON_FAIL=abort failed, per window.",
		Type: "counter",
	}
	skips := &Metric{
		Name: "sball_addition_hook_skips_total",
		Help: "Skipped hook runs (disabled, RUN_ONCE, target) per phase/hook inside the window.",
		Type: "counter",
	}

	type counterKey struct{ phase, hook string }
	failCount := map[string]map[counterKey]float64{}
	retryCount := map[string]map[counterKey]float64{}
	skipCount := map[string]map[counterKey]float64{}
	timeouts := map[string]float64{}
	aborts := map[string]float64{}
	for _, w := range logWindows {
		failCount[w.label] = map[counterKey]float64{}
		retryCount[w.label] = map[counterKey]float64{}
		skipCount[w.label] = map[counterKey]float64{}
		timeouts[w.label] = 0
		aborts[w.label] = 0
	}

	phaseDurations := &Metric{
		Name: "sball_addition_phase_duration_seconds",
		Help: "Duration of the most recent run of a whole phase.",
		Type: "gauge",
	}
	lastPhase := map[string]time.Time{}

	for late := len(events) - 1; late >= 0; late-- {
		ev := events[late]
		ts, err := time.Parse(time.RFC3339, ev.TS)
		if err != nil {
			ts = time.Time{}
		}
		key := counterKey{phase: ev.Phase, hook: ev.Hook}

		for _, w := range logWindows {
			if !ts.IsZero() && now.Sub(ts) > w.dur {
				continue
			}
			switch ev.Event {
			case "fail", "timeout", "give_up":
				if ev.Hook != "" {
					failCount[w.label][key]++
				}
			case "retry":
				if ev.Hook != "" {
					retryCount[w.label][key]++
				}
			case "skip":
				if ev.Hook != "" {
					skipCount[w.label][key]++
				}
			case "poll_timeout":
				timeouts[w.label]++
			case "phase_abort":
				aborts[w.label]++
			}
		}

		// duration of the newest phase_finish per phase
		if ev.Event == "phase_finish" && ev.Phase != "" && !ts.IsZero() {
			if prev, ok := lastPhase[ev.Phase]; !ok || ts.After(prev) {
				lastPhase[ev.Phase] = ts
				phaseDurations.Add(float64(ev.Duration)/1000.0, L("phase", ev.Phase))
			}
		}
	}

	for _, w := range logWindows {
		for key, v := range failCount[w.label] {
			failures.Add(v, L("phase", key.phase), L("hook", key.hook), L("window", w.label))
		}
		for key, v := range retryCount[w.label] {
			retries.Add(v, L("phase", key.phase), L("hook", key.hook), L("window", w.label))
		}
		for key, v := range skipCount[w.label] {
			skips.Add(v, L("phase", key.phase), L("hook", key.hook), L("window", w.label))
		}
		pollTimeouts.Add(timeouts[w.label], L("window", w.label))
		phaseAborts.Add(aborts[w.label], L("window", w.label))
	}

	out := []*Metric{
		info, sballd, watcher,
		phaseHooks, lastSuccess, lastExit, lastDuration, lastSkipped,
		failures, retries, skips, pollTimeouts, phaseAborts, phaseDurations,
	}
	if d.SocketProbed {
		// Only meaningful when the socket was actually queried.
		out = append(out, socket, quorate)
		if d.MasterKnown {
			out = append(out, master)
		}
	}
	return out
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}
