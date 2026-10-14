package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"sball-addition/internal/log"
	"sball-addition/internal/version"
)

// logFilter holds the "sball-addition log" selection criteria.
type logFilter struct {
	hook  string
	phase string
	since time.Time
	level log.Level
}

func (f logFilter) match(rec map[string]any) bool {
	if f.phase != "" && !strings.EqualFold(str(rec["phase"]), f.phase) {
		return false
	}
	if f.hook != "" {
		h := str(rec["hook"])
		if !strings.EqualFold(h, f.hook) && !strings.Contains(h, f.hook) {
			return false
		}
	}
	if !f.since.IsZero() {
		ts := str(rec["ts"])
		t, err := time.Parse(time.RFC3339, ts)
		if err == nil && t.Before(f.since) {
			return false
		}
	}
	if f.level > log.Debug {
		lv, err := log.ParseLevel(str(rec["level"]))
		if err != nil {
			return true
		}
		if lv < f.level {
			return false
		}
	}
	return true
}

func (a *App) cmdLog(args []string) int {
	f := newFlagSet("log", args)
	follow := f.Bool("follow", false, "keep printing new records", "f")
	hookName := f.String("hook", "", "only records of this hook (substring match)")
	phase := f.String("phase", "", "pre-quorate | post-quorate")
	since := f.String("since", "", "duration (10m) or RFC3339 timestamp")
	levelStr := f.String("level", "", "minimum level: debug|info|warn|error")
	limit := f.Int("limit", 200, "show the last N records (0 = all)")
	pretty := f.Bool("pretty", false, "human readable output instead of raw JSON")
	file := f.String("file", "", "read another log file")
	if err := f.Parse(); err != nil {
		return a.usageError("log", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	path := cfg.Paths.LogFile
	if *file != "" {
		path = *file
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(a.Stderr, "%s: no log file at %s yet\n", version.Name, path)
		return ExitNotFound
	}

	filter := logFilter{hook: *hookName, phase: *phase}
	if *since != "" {
		t, err := parseSince(*since)
		if err != nil {
			return a.usageError("log", err)
		}
		filter.since = t
	}
	if *levelStr != "" {
		lv, err := log.ParseLevel(*levelStr)
		if err != nil {
			return a.usageError("log", err)
		}
		filter.level = lv
	}

	emit := func(line string) {
		if *pretty {
			fmt.Fprintln(a.Stdout, prettyLine(line))
			return
		}
		fmt.Fprintln(a.Stdout, line)
	}

	fh, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
	defer fh.Close()

	if *limit > 0 {
		for _, line := range tailLines(fh, *limit*5) { // over-read: filtering happens next
			if lineMatches(line, filter) {
				emit(line)
			}
		}
	} else {
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			if lineMatches(line, filter) {
				emit(line)
			}
		}
		if err := sc.Err(); err != nil {
			fmt.Fprintf(a.Stderr, "%s: read %s: %v\n", version.Name, path, err)
			return ExitFailure
		}
		if !*follow {
			return ExitOK
		}
	}

	if !*follow {
		return ExitOK
	}

	ctx, cancel := signalContext()
	defer cancel()

	offset, _ := fh.Seek(0, io.SeekEnd)
	fmt.Fprintf(a.Stderr, "-- following %s (Ctrl-C to stop) --\n", path)
	reader := bufio.NewReader(fh)
	for {
		select {
		case <-ctx.Done():
			return ExitOK
		case <-time.After(250 * time.Millisecond):
		}
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.Size() < offset { // truncated or rotated: start over
			if _, err := fh.Seek(0, io.SeekStart); err == nil {
				reader.Reset(fh)
				offset = 0
			}
		}
		for {
			line, err := reader.ReadString('\n')
			if line != "" {
				offset += int64(len(line))
				trimmed := strings.TrimRight(line, "\n")
				if lineMatches(trimmed, filter) {
					emit(trimmed)
				}
			}
			if err != nil {
				break
			}
		}
	}
}

func lineMatches(line string, filter logFilter) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return false
	}
	if filter == (logFilter{}) {
		return true
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		// Non JSON lines (e.g. a shell hook writing to the log) only pass when
		// no structured filter is active.
		return filter.phase == "" && filter.hook == "" && filter.since.IsZero()
	}
	return filter.match(rec)
}

func tailLines(r io.Reader, n int) []string {
	if n <= 0 {
		n = 100
	}
	ring := make([]string, 0, n)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if len(ring) < n {
			ring = append(ring, sc.Text())
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = sc.Text()
	}
	return ring
}

// parseSince accepts "10m", "2h", "1d", "2026-10-14T12:00:00Z" and
// "2026-10-14 12:00:00".
func parseSince(s string) (time.Time, error) {
	v := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "ago"))
	if d, err := time.ParseDuration(v); err == nil {
		return time.Now().Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("--since: %q is not a duration (10m, 2h) or a timestamp", s)
}

// prettyLine renders one JSON record as compact readable text.
func prettyLine(line string) string {
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &rec); err != nil {
		return line
	}
	correlated := []string{"phase", "hook"}
	level := str(rec["level"])
	what := []string{}
	for _, k := range correlated {
		if v := str(rec[k]); v != "" {
			what = append(what, v)
		}
	}
	keys := make([]string, 0, len(rec))
	for k := range rec {
		switch k {
		case "ts", "level", "pid", "phase", "hook", "event":
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var extras []string
	for _, k := range keys {
		extras = append(extras, fmt.Sprintf("%s=%v", k, rec[k]))
	}
	return fmt.Sprintf("%s  %-5s  %-38s  %-14s  %s",
		str(rec["ts"]), level, strings.Join(what, "/"), str(rec["event"]), strings.Join(extras, " "))
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
