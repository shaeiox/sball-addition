package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func render(t *testing.T, ms []*Metric) string {
	t.Helper()
	var buf bytes.Buffer
	if err := Render(&buf, ms); err != nil {
		t.Fatalf("Render: %v", err)
	}
	return buf.String()
}

// findLine returns the sample line with the given prefix ("" when absent).
func findLine(out, prefix string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func TestRenderFormat(t *testing.T) {
	m := &Metric{Name: "x_total", Help: "help text", Type: "counter"}
	m.Add(3, L("b", "2"), L("a", "1"))
	out := render(t, []*Metric{m})

	if !strings.Contains(out, "# HELP x_total help text\n") {
		t.Errorf("HELP line missing:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE x_total counter\n") {
		t.Errorf("TYPE line missing:\n%s", out)
	}
	// labels are sorted alphabetically: a before b
	if !strings.Contains(out, `x_total{a="1",b="2"} 3`) {
		t.Errorf("sample line wrong:\n%s", out)
	}
}

func TestRenderSortsMetricsAndSkipsEmpty(t *testing.T) {
	z := &Metric{Name: "zeta", Type: "gauge"}
	z.Add(1)
	a := &Metric{Name: "alpha", Type: "gauge"}
	a.Add(2)
	empty := &Metric{Name: "empty", Help: "no samples", Type: "gauge"}

	out := render(t, []*Metric{z, empty, a})
	if strings.Index(out, "alpha") > strings.Index(out, "zeta") {
		t.Errorf("metrics should be sorted by name:\n%s", out)
	}
	if strings.Contains(out, "empty") {
		t.Errorf("metrics without samples must not be rendered:\n%s", out)
	}
	// a nil metric must not panic
	if err := Render(&bytes.Buffer{}, []*Metric{nil}); err != nil {
		t.Errorf("Render(nil): %v", err)
	}
}

func TestValueFormatting(t *testing.T) {
	cases := map[float64]string{
		0:      "0",
		1:      "1",
		-1:     "-1",
		42:     "42",
		0.5:    "0.5",
		12.345: "12.345",
	}
	for value, want := range cases {
		m := &Metric{Name: "m"}
		m.Add(value)
		line := strings.TrimSpace(findLine(render(t, []*Metric{m}), "m "))
		if line != "m "+want {
			t.Errorf("value %v rendered as %q, want %q", value, line, "m "+want)
		}
	}
}

func TestLabelEscaping(t *testing.T) {
	m := &Metric{Name: "m"}
	m.Add(1, L("path", `/var/log/a"b\c`), L("multi", "line1\nline2"))
	out := render(t, []*Metric{m})
	if !strings.Contains(out, `path="/var/log/a\"b\\c"`) {
		t.Errorf("quotes and backslashes must be escaped:\n%s", out)
	}
	if !strings.Contains(out, `multi="line1\nline2"`) {
		t.Errorf("newlines must be escaped as \\n:\n%s", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("a sample must stay on one line:\n%s", out)
	}
}

func TestParseLogLines(t *testing.T) {
	lines := []string{
		`{"ts":"2026-10-14T12:00:00Z","level":"info","phase":"post-quorate","hook":"a","event":"finish","exit":0}`,
		"not json at all",
		"",
		`{"ts":"2026-10-14T12:00:01Z","event":"fail","phase":"post-quorate","hook":"a","exit":1}`,
	}
	events := ParseLogLines(lines)
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[1].Event != "fail" || events[1].Exit != 1 || events[1].Hook != "a" {
		t.Errorf("second event = %+v", events[1])
	}
}

func TestCollectExposesNodeState(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	deps := &Deps{
		Version:      "1.0.0",
		Host:         "spongebob",
		SballdPID:    1234,
		WatcherPID:   1250,
		SocketOK:     true,
		SocketProbed: true,
		Quorate:      true,
		MasterKnown:  true,
		IsMaster:     true,
		Pre:          PhaseInfo{Enabled: true, Hooks: 2, On: 1},
		Post:         PhaseInfo{Enabled: false, Hooks: 4, On: 3},
		Now:          now,
	}
	out := render(t, Collect(deps))

	checks := []string{
		`sball_addition_sballd_up 1`,
		`sball_addition_pvm_socket_reachable 1`,
		`sball_addition_quorate 1`,
		`sball_addition_is_master 1`,
		`sball_addition_watcher_running 1`,
		`sball_addition_hooks{phase="pre-quorate",state="installed"} 2`,
		`sball_addition_hooks{phase="pre-quorate",state="enabled"} 1`,
		`sball_addition_hooks{phase="pre-quorate",state="phase_enabled"} 1`,
		`sball_addition_hooks{phase="post-quorate",state="phase_enabled"} 0`,
		`sball_addition_info{config="unknown",host="spongebob",version="1.0.0"} 1`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("missing sample %q\n%s", want, out)
		}
	}
}

func TestCollectOmitsUnprobedSocketSeries(t *testing.T) {
	deps := &Deps{
		Version:   "1.0.0",
		Host:      "spongebob",
		SballdPID: 1234,
		Pre:       PhaseInfo{Enabled: true, Hooks: 1, On: 1},
		Now:       time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
		// SocketProbed deliberately false: nothing was queried, so reporting
		// "not quorate" would be a lie that pages somebody.
	}
	out := render(t, Collect(deps))
	for _, unwanted := range []string{
		"sball_addition_pvm_socket_reachable",
		"sball_addition_quorate",
		"sball_addition_is_master",
	} {
		if strings.Contains(out, unwanted) {
			t.Errorf("%s must be omitted when the socket was not probed:\n%s", unwanted, out)
		}
	}
	if !strings.Contains(out, "sball_addition_sballd_up 1") {
		t.Errorf("local checks must survive:\n%s", out)
	}
}

func TestCollectMarksSocketDownWhenProbed(t *testing.T) {
	deps := &Deps{
		SocketProbed: true, // probed, but the node did not answer: report 0
		Now:          time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
	}
	out := render(t, Collect(deps))
	for _, want := range []string{
		"sball_addition_pvm_socket_reachable 0",
		"sball_addition_quorate 0",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sball_addition_is_master") {
		t.Errorf("is_master needs a known master id:\n%s", out)
	}
}

func TestCollectHookLastRunMetrics(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	ok := now.Add(-5 * time.Minute)
	deps := &Deps{
		Now: now,
		LastRuns: map[string]LastRun{
			"post-quorate/ok": {
				Hook: "ok", Phase: "post-quorate", Event: "finish", Exit: 0,
				DurationMS: 2500, At: ok,
			},
			"post-quorate/broken": {
				Hook: "broken", Phase: "post-quorate", Event: "timeout", Exit: 143,
				DurationMS: 60000, TimedOut: true, At: ok,
			},
		},
	}
	out := render(t, Collect(deps))

	if !strings.Contains(out, `hook="ok",phase="post-quorate"} 1791978900`) &&
		!strings.Contains(out, `hook="ok",phase="post-quorate"} 1`) {
		if !strings.Contains(out, "sball_addition_hook_last_success_timestamp_seconds{hook=\"ok\"") {
			t.Errorf("successful hook should expose a last-success timestamp:\n%s", out)
		}
	}
	// the success timestamp must be the run time (1791978900 = now-5m), not 0
	if !strings.Contains(out, `sball_addition_hook_last_success_timestamp_seconds{hook="ok",phase="post-quorate"} 1791978900`) {
		t.Errorf("a hook that finished successfully must expose its run timestamp:\n%s", out)
	}
	// a timed out hook reports exit 143, duration 60s and timed_out=1
	if !strings.Contains(out, `sball_addition_hook_last_exit_code{hook="broken",phase="post-quorate"} 143`) {
		t.Errorf("exit code missing:\n%s", out)
	}
	if !strings.Contains(out, `sball_addition_hook_last_duration_seconds{hook="broken",phase="post-quorate"} 60`) {
		t.Errorf("duration missing:\n%s", out)
	}
	if !strings.Contains(out, `sball_addition_hook_last_run_timed_out{hook="broken",phase="post-quorate"} 1`) {
		t.Errorf("timed_out flag missing:\n%s", out)
	}
	// never successful -> explicit zero sample
	if !strings.Contains(out, `hook="broken",phase="post-quorate"} 0`) {
		t.Errorf("a hook that never succeeded should expose 0:\n%s", out)
	}
}

func TestCollectLogCountersAndWindows(t *testing.T) {
	now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	line := func(ts, event, hook string, ms int) string {
		return `{"ts":"` + ts + `","event":"` + event + `","phase":"post-quorate","hook":"` + hook + `","duration_ms":` + itoa(ms) + `}`
	}
	deps := &Deps{
		Now: now,
		LogLines: []string{
			line("2026-10-14T11:59:00Z", "fail", "a", 10),       // 1h window
			line("2026-10-14T11:58:00Z", "retry", "a", 0),       // 1h window
			line("2026-10-14T11:57:00Z", "timeout", "a", 60000), // 1h window
			line("2026-10-14T11:58:30Z", "skip", "b", 0),        // 1h window
			line("2026-10-13T20:00:00Z", "fail", "c", 10),       // 24h window only (16h ago)
			line("2026-10-10T10:00:00Z", "give_up", "d", 0),     // 7d window only
			line("2026-10-14T11:59:30Z", "poll_timeout", "", 0),
			line("2026-10-14T11:59:40Z", "phase_abort", "a", 0),
			line("2026-10-14T11:50:00Z", "phase_finish", "", 1200),
		},
	}
	out := render(t, Collect(deps))

	checks := []string{
		`sball_addition_hook_failures_total{hook="a",phase="post-quorate",window="1h"} 2`,
		`sball_addition_hook_failures_total{hook="c",phase="post-quorate",window="24h"} 1`,
		`sball_addition_hook_failures_total{hook="d",phase="post-quorate",window="7d"} 1`,
		`sball_addition_hook_retries_total{hook="a",phase="post-quorate",window="1h"} 1`,
		`sball_addition_hook_skips_total{hook="b",phase="post-quorate",window="1h"} 1`,
		`sball_addition_poll_timeouts_total{window="1h"} 1`,
		`sball_addition_phase_aborts_total{window="1h"} 1`,
		`sball_addition_phase_duration_seconds{phase="post-quorate"} 1.2`,
	}
	for _, want := range checks {
		if !strings.Contains(out, want) {
			t.Errorf("missing sample %q\n%s", want, out)
		}
	}
	// the old records must not leak into the short window
	if strings.Contains(out, `hook="c",phase="post-quorate",window="1h"}`) {
		t.Errorf("records older than the window must be excluded:\n%s", out)
	}
}

func TestCollectWithoutStateIsStillValid(t *testing.T) {
	out := render(t, Collect(&Deps{Now: time.Now(), SocketProbed: true}))
	for _, want := range []string{
		"sball_addition_sballd_up 0",
		"sball_addition_quorate 0",
		"sball_addition_watcher_running 0",
		`sball_addition_info{config="unknown",host="unknown",version="unknown"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in the empty-state output:\n%s", want, out)
		}
	}
}

func TestReadLastRunsFile(t *testing.T) {
	dir := t.TempDir()
	runs := ReadLastRunsFile(dir + "/missing.json")
	if len(runs) != 0 {
		t.Errorf("missing file must yield an empty map, got %v", runs)
	}
	path := dir + "/last-run.json"
	if err := writeFile(t, path, `{"post-quorate/x":{"hook":"x","phase":"post-quorate","event":"finish"}}`); err != nil {
		t.Fatal(err)
	}
	runs = ReadLastRunsFile(path)
	if rec, ok := runs["post-quorate/x"]; !ok || rec.Event != "finish" {
		t.Errorf("runs = %+v", runs)
	}
	if err := writeFile(t, path, `{not json`); err != nil {
		t.Fatal(err)
	}
	if runs = ReadLastRunsFile(path); len(runs) != 0 {
		t.Errorf("a damaged registry must yield an empty map, got %v", runs)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
