package log

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decode(t *testing.T, line string) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("record is not valid JSON: %v\n%s", err, line)
	}
	return rec
}

func TestJSONLines(t *testing.T) {
	var buf bytes.Buffer
	l := NewStderr(&buf, Debug)
	l.Info("start", String("phase", "pre-quorate"), String("hook", "10-storage-mount"), String("host", "spongebob"))
	l.Error("fail", Int("exit", 1), String("stderr", "connection refused"), Int("retry", 1))
	l.Debug("stdout", String("output", "multi\nline"))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), buf.String())
	}
	rec := decode(t, lines[0])
	if rec["event"] != "start" || rec["phase"] != "pre-quorate" || rec["hook"] != "10-storage-mount" {
		t.Errorf("record = %v", rec)
	}
	if rec["level"] != "info" {
		t.Errorf("level = %v", rec["level"])
	}
	if ts, ok := rec["ts"].(string); !ok || !strings.HasSuffix(ts, "Z") {
		t.Errorf("ts = %v, want an RFC3339 UTC timestamp", rec["ts"])
	}
	// key order: phase/hook before event, other fields after
	if !strings.HasPrefix(lines[0], `{"ts":"`) {
		t.Errorf("ts should come first: %s", lines[0])
	}
	if strings.Index(lines[0], `"phase"`) > strings.Index(lines[0], `"event"`) {
		t.Errorf("correlated fields should precede the event: %s", lines[0])
	}
	if strings.Index(lines[0], `"event"`) > strings.Index(lines[0], `"host"`) {
		t.Errorf("other fields should follow the event: %s", lines[0])
	}

	fail := decode(t, lines[1])
	if fail["exit"] != float64(1) || fail["stderr"] != "connection refused" {
		t.Errorf("fail record = %v", fail)
	}
	multi := decode(t, lines[2])
	if multi["output"] != "multi\nline" {
		t.Errorf("newlines must be escaped, got %v", multi["output"])
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	l := NewStderr(&buf, Warn)
	l.Debug("debug-event")
	l.Info("info-event")
	l.Warn("warn-event")
	l.Error("error-event")

	out := buf.String()
	if strings.Contains(out, "debug-event") || strings.Contains(out, "info-event") {
		t.Errorf("records below the level must be dropped:\n%s", out)
	}
	if !strings.Contains(out, "warn-event") || !strings.Contains(out, "error-event") {
		t.Errorf("records at or above the level must be kept:\n%s", out)
	}
	if l.Level() != Warn {
		t.Errorf("Level = %v", l.Level())
	}
	l.SetLevel(Error)
	if !strings.Contains(out, "warn-event") {
		// already written, only new records are affected
		t.Error("previous output must not change")
	}
	l.Warn("late-warn")
	if strings.Contains(buf.String(), "late-warn") {
		t.Error("records below the new level must be dropped")
	}
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]Level{
		"debug": Debug, "INFO": Info, "warn": Warn, "warning": Warn, "error": Error, "": Info,
	} {
		got, err := ParseLevel(in)
		if err != nil {
			t.Errorf("ParseLevel(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := ParseLevel("loud"); err == nil {
		t.Error("invalid levels must be rejected")
	}
}

func TestFileLoggingAndReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "sball-addition.log")
	l, err := Open(Options{Path: path, Level: Info})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	l.Info("first")
	if l.Path() != path {
		t.Errorf("Path = %q", l.Path())
	}

	// Simulate logrotate: move the file away and reopen.
	rotated := path + ".1"
	if err := os.Rename(path, rotated); err != nil {
		t.Fatal(err)
	}
	if err := l.Reopen(); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	l.Info("second")
	if err := l.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	oldData, _ := os.ReadFile(rotated)
	if !strings.Contains(string(oldData), "first") {
		t.Error("the rotated file should keep the first record")
	}
	newData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("after Reopen the new log file must exist: %v", err)
	}
	if !strings.Contains(string(newData), "second") || strings.Contains(string(newData), "first") {
		t.Errorf("new log file content:\n%s", newData)
	}
}

func TestOpenFallsBackToStderr(t *testing.T) {
	// /proc is not a directory we can create files in.
	l, err := Open(Options{Path: "/proc/definitely/not/writable.log", Level: Info})
	if err == nil {
		t.Skip("unexpectedly writable, cannot test the fallback")
	}
	if l == nil {
		t.Fatal("a logger must be returned even when the file cannot be opened")
	}
	l.Info("still works")
}

func TestNilLoggerIsSafe(t *testing.T) {
	var l *Logger
	l.Info("no panic")
	l.Error("no panic either", String("k", "v"))
	if l.Enabled(Error) {
		t.Error("a nil logger is never enabled")
	}
}

func TestTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log")
	if err := os.WriteFile(path, []byte("1\n2\n3\n4\n5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := Tail(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0] != "4" || lines[1] != "5" {
		t.Errorf("Tail = %v", lines)
	}
	if _, err := Tail(filepath.Join(dir, "missing"), 5); err == nil {
		t.Error("a missing file must be an error")
	}
}

func TestConcurrentWrites(t *testing.T) {
	var buf bytes.Buffer
	l := NewStderr(&buf, Debug)
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(n int) {
			defer func() { done <- struct{}{} }()
			for j := 0; j < 50; j++ {
				l.Info("evt", Int("worker", n), Int("n", j))
			}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 400 {
		t.Fatalf("got %d lines, want 400", len(lines))
	}
	for _, line := range lines {
		decode(t, line)
	}
}
