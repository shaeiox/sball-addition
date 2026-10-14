package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sball-addition/internal/hook"
	"sball-addition/internal/log"
	"sball-addition/internal/metrics"
	"sball-addition/internal/proc"
	"sball-addition/internal/pvm"
	"sball-addition/internal/runner"
	"sball-addition/internal/version"
)

// metricsWindow bounds how much of the log is read for the counter metrics.
// 7 days of JSON lines is a few MB at most, which is nothing for a one minute
// timer, and it keeps the exporter stateless.
const metricsWindow = 7*24*time.Hour + time.Minute

func (a *App) cmdMetrics(args []string) int {
	f := newFlagSet("metrics", args)
	out := f.String("out", "", "write to this file (textfile collector) instead of stdout")
	noSocket := f.Bool("no-socket", false, "do not query the PVM socket (the socket, quorate and master series are omitted)")
	asJSON := f.Bool("json", false, "print the raw snapshot instead of Prometheus metrics")
	pretty := f.Bool("pretty", false, "pretty print the --json output")
	if err := f.Parse(); err != nil {
		return a.usageError("metrics", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}

	deps := &metrics.Deps{
		Version:    version.Version,
		Host:       pvm.LocalName(),
		ConfigFile: cfg.Paths.ConfigFile,
		LogFile:    cfg.Paths.LogFile,
		Now:        time.Now().UTC(),
	}

	// ------------------------------------------------ last run registry ----
	deps.LastRuns = map[string]metrics.LastRun{}
	if runs, err := runner.ReadLastRuns(cfg.Paths.LastRunFile()); err == nil {
		for k, rec := range runs {
			deps.LastRuns[k] = metrics.LastRun{
				Hook:       rec.Hook,
				Phase:      rec.Phase,
				Event:      rec.Event,
				Exit:       rec.Exit,
				Attempts:   rec.Attempts,
				DurationMS: rec.DurationMS,
				TimedOut:   rec.TimedOut,
				At:         rec.At,
			}
		}
	}

	// ------------------------------------------------------- log window ----
	for _, line := range logLinesSince(cfg.Paths.LogFile, deps.Now.Add(-metricsWindow)) {
		deps.LogLines = append(deps.LogLines, line)
	}

	// --------------------------------------------------------- hook sets ----
	pre := hook.LoadPhase(cfg.Paths.PreDir, hook.PhasePre)
	post := hook.LoadPhase(cfg.Paths.PostDir, hook.PhasePost)
	deps.Pre = metrics.PhaseInfo{Enabled: cfg.PreQuorate.Enabled, Hooks: len(pre.Hooks), On: len(pre.Enabled())}
	deps.Post = metrics.PhaseInfo{Enabled: cfg.PostQuorate.Enabled, Hooks: len(post.Hooks), On: len(post.Enabled())}

	// ------------------------------------------------------------ node -----
	deps.SballdPID = proc.Find("sballd")
	if pid := readPidFile(cfg.Paths.PidFile()); pid > 0 && proc.Alive(pid) {
		deps.WatcherPID = pid
	} else if holder := lockHolder(cfg); holder > 0 && proc.Alive(holder) {
		deps.WatcherPID = holder
	}
	deps.SocketProbed = !*noSocket
	if !*noSocket {
		status, err := a.queryPVM(cfg)
		switch {
		case err == nil:
			deps.SocketOK = true
			deps.Quorate = status.Quorate
			deps.MasterKnown = status.MasterKnown
			deps.IsMaster = status.IsMaster
		case errors.Is(err, pvm.ErrUnavailable):
			lg.Debug("metrics_socket_unavailable")
		default:
			lg.Warn("metrics_socket_error", log.String("error", err.Error()))
		}
	}

	if *asJSON {
		// socket/quorate/is_master stay null when the probe was skipped, so a
		// JSON consumer sees "unknown" and not "down".
		var socketAny, quorateAny, masterAny any
		if deps.SocketProbed {
			socketAny, quorateAny = deps.SocketOK, deps.Quorate
			if deps.MasterKnown {
				masterAny = deps.IsMaster
			}
		}
		snapshot := map[string]any{
			"version":          deps.Version,
			"host":             deps.Host,
			"sballd_pid":       deps.SballdPID,
			"watcher_pid":      deps.WatcherPID,
			"socket_reachable": socketAny,
			"quorate":          quorateAny,
			"is_master":        masterAny,
			"pre_quorate":      deps.Pre,
			"post_quorate":     deps.Post,
			"last_runs":        deps.LastRuns,
			"log_records":      len(deps.LogLines),
		}
		_ = pretty // printJSON already indents; kept for symmetry with "log"
		a.printJSON(snapshot)
		return ExitOK
	}

	rendered := &strings.Builder{}
	if err := metrics.Render(rendered, metrics.Collect(deps)); err != nil {
		return a.fail("render metrics: %v", err)
	}

	if *out == "" {
		fmt.Fprint(a.Stdout, rendered.String())
		return ExitOK
	}
	if err := writeFileAtomic(*out, []byte(rendered.String()), 0o644); err != nil {
		return a.fail("write %s: %v", *out, err)
	}
	lg.Debug("metrics_written", log.String("file", *out), log.Int("bytes", rendered.Len()))
	return ExitOK
}

// logLinesSince reads the tail of the log and keeps the records newer than
// since. The log is read from the end backwards in chunks so a multi-GB file
// stays cheap to scan.
func logLinesSince(path string, since time.Time) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if ts := extractTS(line); !ts.IsZero() && ts.Before(since) {
			continue
		}
		out = append(out, line)
	}
	return out
}

// extractTS pulls the "ts" field out of a JSON log record without decoding the
// whole line (this runs for every record in the window).
func extractTS(line string) time.Time {
	const key = `"ts":"`
	i := strings.Index(line, key)
	if i < 0 {
		return time.Time{}
	}
	rest := line[i+len(key):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339, rest[:j])
	if err != nil {
		return time.Time{}
	}
	return ts
}

// writeFileAtomic writes a file via a temp file + rename, so a scrape never
// sees a half written metrics file.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
