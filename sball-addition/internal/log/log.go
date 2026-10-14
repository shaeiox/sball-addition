// Package log implements the structured JSON-lines logger used by every
// sball-addition component.
//
// Every record is a single line of JSON with a stable shape:
//
//	{"ts":"2026-10-14T12:00:00Z","level":"info","pid":1234,"phase":"pre-quorate",
//	 "hook":"10-storage-mount","event":"start","host":"spongebob"}
//
// The logger is safe for concurrent use and never blocks on a broken log
// file: if the configured file cannot be (re)opened, records fall back to
// stderr so an operator still sees what happened.
package log

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Level is a log severity. The textual form is what appears in the "level"
// field of every record.
type Level int

// Severity levels, ordered from most to least verbose.
const (
	Debug Level = iota
	Info
	Warn
	Error
)

// ParseLevel converts a textual level (as used in sball-addition.conf) into a
// Level. Accepted values are case-insensitive: debug, info, warn/warning,
// error.
func ParseLevel(s string) (Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return Debug, nil
	case "info", "":
		return Info, nil
	case "warn", "warning":
		return Warn, nil
	case "error", "err":
		return Error, nil
	default:
		return Info, fmt.Errorf("invalid log level %q (expected debug|info|warn|error)", s)
	}
}

// String renders the level as it appears in a log record.
func (l Level) String() string {
	switch l {
	case Debug:
		return "debug"
	case Info:
		return "info"
	case Warn:
		return "warn"
	case Error:
		return "error"
	default:
		return "info"
	}
}

// Field is an ordered key/value pair appended to a log record.
type Field struct {
	K string
	V any
}

// String builds a string field.
func String(k, v string) Field { return Field{k, v} }

// Int builds an integer field.
func Int(k string, v int) Field { return Field{k, v} }

// Int64 builds a 64 bit integer field.
func Int64(k string, v int64) Field { return Field{k, v} }

// Bool builds a boolean field.
func Bool(k string, v bool) Field { return Field{k, v} }

// Any builds a field from an arbitrary JSON-encodable value.
func Any(k string, v any) Field { return Field{k, v} }

// Millis builds an integer field from a duration expressed in milliseconds.
func Millis(k string, d time.Duration) Field { return Field{k, d.Milliseconds()} }

// Logger writes JSON-lines records.
type Logger struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	buf    *bufio.Writer
	out    io.Writer // active sink (file, or fallback)
	level  Level
	pid    int
	fields []Field // fields prepended to every record
	now    func() time.Time
}

// Options configures a Logger.
type Options struct {
	// Path is the log file. Empty means "stderr only".
	Path string
	// Level is the minimum severity that gets written.
	Level Level
	// Stderr mirrors every record to stderr when true.
	Stderr bool
	// Fields are prepended to every record (e.g. component=watcher).
	Fields []Field
}

// Open creates a logger writing to the file at opt.Path (creating parent
// directories as needed) and optionally mirroring to stderr.
//
// If the log file cannot be opened the returned logger still works and writes
// to stderr; the returned error describes the fallback so callers can surface
// it. A nil error means the file was opened successfully.
func Open(opt Options) (*Logger, error) {
	l := &Logger{
		level:  opt.Level,
		pid:    os.Getpid(),
		fields: opt.Fields,
		now:    func() time.Time { return time.Now().UTC() },
	}
	var openErr error
	if opt.Path != "" {
		if err := l.openFile(opt.Path); err != nil {
			openErr = err
		}
	}
	if l.file == nil {
		l.out = os.Stderr
	}
	if opt.Stderr {
		// Keep the file as the primary sink but also mirror to stderr.
		if l.file != nil {
			l.out = io.MultiWriter(l.file, os.Stderr)
		} else {
			l.out = os.Stderr
		}
	}
	return l, openErr
}

// NewStderr returns a logger that writes to the supplied writer only. It is
// primarily used by tests and by commands invoked through --log-to-stderr.
func NewStderr(w io.Writer, level Level) *Logger {
	if w == nil {
		w = os.Stderr
	}
	return &Logger{
		level: level,
		pid:   os.Getpid(),
		out:   w,
		now:   func() time.Time { return time.Now().UTC() },
	}
}

func (l *Logger) openFile(path string) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create log directory %s: %w", dir, err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	l.file = f
	l.buf = bufio.NewWriterSize(f, 32*1024)
	l.path = path
	l.out = l.buf
	return nil
}

// Path returns the configured log file path ("" when logging to stderr only).
func (l *Logger) Path() string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// Level returns the current minimum severity.
func (l *Logger) Level() Level {
	if l == nil {
		return Info
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// SetLevel changes the minimum severity written.
func (l *Logger) SetLevel(lv Level) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = lv
}

// Reopen closes and re-opens the log file, which is how logrotate is
// supported (the watcher does this on SIGHUP).
func (l *Logger) Reopen() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.path == "" {
		return nil
	}
	if l.buf != nil {
		_ = l.buf.Flush()
	}
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
	if err := l.openFile(l.path); err != nil {
		l.out = os.Stderr
		return err
	}
	return nil
}

// Close flushes and closes the log file.
func (l *Logger) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	var errs []error
	if l.buf != nil {
		if err := l.buf.Flush(); err != nil {
			errs = append(errs, err)
		}
		l.buf = nil
	}
	if l.file != nil {
		if err := l.file.Close(); err != nil {
			errs = append(errs, err)
		}
		l.file = nil
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// Enabled reports whether a record at level lv would be written.
func (l *Logger) Enabled(lv Level) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return lv >= l.level
}

// Log writes one record. It is always safe to call on a nil *Logger: the call
// is a no-op, which keeps optional logging out of the call sites.
func (l *Logger) Log(lv Level, event string, fields ...Field) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if lv < l.level {
		return
	}
	if l.out == nil {
		return
	}
	line := l.encodeLocked(lv, event, fields)
	if _, err := l.out.Write(line); err != nil {
		// Last resort: never lose a record silently because the sink died.
		fmt.Fprintf(os.Stderr, "%s", line)
		l.out = os.Stderr
	}
	if l.buf != nil {
		_ = l.buf.Flush()
	}
}

// Debug logs at debug level.
func (l *Logger) Debug(event string, fields ...Field) { l.Log(Debug, event, fields...) }

// Info logs at info level.
func (l *Logger) Info(event string, fields ...Field) { l.Log(Info, event, fields...) }

// Warn logs at warn level.
func (l *Logger) Warn(event string, fields ...Field) { l.Log(Warn, event, fields...) }

// Error logs at error level.
func (l *Logger) Error(event string, fields ...Field) { l.Log(Error, event, fields...) }

// correlatedFieldKeys are emitted before "event" so that records read like the
// documented examples: {"ts":..,"phase":..,"hook":..,"event":..,"host":..}.
var correlatedFieldKeys = map[string]bool{"phase": true, "hook": true}

func (l *Logger) encodeLocked(lv Level, event string, fields []Field) []byte {
	var correlated, rest []Field
	for _, f := range fields {
		if correlatedFieldKeys[f.K] {
			correlated = append(correlated, f)
			continue
		}
		rest = append(rest, f)
	}

	var b []byte
	b = append(b, '{')
	b = appendJSONField(b, "ts", l.now().Format("2006-01-02T15:04:05Z"), false)
	b = appendJSONField(b, "level", lv.String(), true)
	if l.pid > 0 {
		b = appendJSONField(b, "pid", l.pid, true)
	}
	for _, f := range l.fields {
		b = appendField(b, f)
	}
	for _, f := range correlated {
		b = appendField(b, f)
	}
	b = appendJSONField(b, "event", event, true)
	for _, f := range rest {
		b = appendField(b, f)
	}
	b = append(b, '}', '\n')
	return b
}

func appendField(b []byte, f Field) []byte {
	if f.K == "" {
		return b
	}
	return appendJSONField(b, f.K, f.V, true)
}

func appendJSONField(b []byte, key string, val any, comma bool) []byte {
	if comma {
		b = append(b, ',')
	}
	b = appendString(b, key)
	b = append(b, ':')
	b = appendValue(b, val)
	return b
}

func appendString(b []byte, s string) []byte {
	enc, err := json.Marshal(s)
	if err != nil {
		enc = []byte(`""`)
	}
	return append(b, enc...)
}

func appendValue(b []byte, v any) []byte {
	if s, ok := v.(string); ok {
		return appendString(b, s)
	}
	enc, err := json.Marshal(v)
	if err != nil {
		return appendString(b, fmt.Sprintf("%v", v))
	}
	return append(b, enc...)
}

// Tail returns the last n lines of the log file at path. It is used by the
// status command and by hook failure logging.
func Tail(path string, n int) ([]string, error) {
	if n <= 0 {
		n = 20
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ring []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if len(ring) < n {
			ring = append(ring, line)
			continue
		}
		copy(ring, ring[1:])
		ring[len(ring)-1] = line
	}
	if err := sc.Err(); err != nil && !errors.Is(err, io.EOF) {
		return ring, err
	}
	return ring, nil
}
