// Package hook implements discovery, parsing, validation and ordering of
// sball-addition hook scripts.
//
// A hook is an executable script with a machine readable header:
//
//	#!/bin/bash
//	### BEGIN SBALL-ADDITION INFO
//	# HOOK: post-quorate
//	# NAME: register-monitoring
//	# DESCRIPTION: Register node in monitoring system
//	# TIMEOUT: 60
//	# RETRY: 2
//	# RETRY_BACKOFF: 5
//	# ON_FAIL: continue
//	# RUN_ONCE: false
//	# AFTER: 10-network-check
//	# TARGET: local
//	# ENABLED: true
//	### END SBALL-ADDITION INFO
package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Phase identifies when a hook runs.
type Phase string

// Supported phases.
const (
	PhasePre  Phase = "pre-quorate"
	PhasePost Phase = "post-quorate"
)

// AllPhases lists the phases in lifecycle order.
func AllPhases() []Phase { return []Phase{PhasePre, PhasePost} }

// Valid reports whether p is a known phase.
func (p Phase) Valid() bool { return p == PhasePre || p == PhasePost }

// String returns the phase name.
func (p Phase) String() string { return string(p) }

// DirName returns the hook directory name for the phase ("post-quorate.d").
func (p Phase) DirName() string { return string(p) + ".d" }

// ParsePhase converts a CLI/header value into a Phase.
func ParsePhase(s string) (Phase, error) {
	p := Phase(strings.ToLower(strings.TrimSpace(s)))
	if !p.Valid() {
		return "", fmt.Errorf("invalid phase %q (expected pre-quorate|post-quorate)", s)
	}
	return p, nil
}

// Target controls on which nodes a hook is executed.
type Target string

// Supported targets.
const (
	TargetLocal  Target = "local"
	TargetMaster Target = "master"
	TargetAll    Target = "all"
)

// Valid reports whether t is a known target.
func (t Target) Valid() bool {
	return t == TargetLocal || t == TargetMaster || t == TargetAll
}

// OnFailure policies.
const (
	OnFailAbort    = "abort"
	OnFailContinue = "continue"
)

// Header markers.
const (
	headerBegin = "### BEGIN SBALL-ADDITION INFO"
	headerEnd   = "### END SBALL-ADDITION INFO"
)

// MaxHeaderBytes bounds how much of a hook file is inspected for a header.
const MaxHeaderBytes = 64 * 1024

// Meta is the parsed header of a hook plus derived filesystem information.
type Meta struct {
	Phase        Phase
	Name         string
	Description  string
	Timeout      int
	Retry        int
	RetryBackoff int
	OnFail       string // "" means "inherit the phase policy"
	RunOnce      bool
	After        string
	Target       Target
	Enabled      bool

	// Derived from the file on disk.
	Path             string
	FileName         string
	Order            int // numeric filename prefix (10 for 10-foo.sh), 0 if absent
	OrderSet         bool
	Executable       bool
	DisabledBySuffix bool

	// raw holds every header field exactly as written, which lets the
	// enable/disable commands rewrite the header without losing comments.
	raw map[string]string
	// positions of the header lines in the file (0 based).
	lineIndex map[string]int
}

// Field returns a raw header value as written in the file.
func (m *Meta) Field(name string) (string, bool) {
	v, ok := m.raw[strings.ToUpper(strings.TrimSpace(name))]
	return v, ok
}

// Raw returns a copy of all header fields.
func (m *Meta) Raw() map[string]string {
	out := make(map[string]string, len(m.raw))
	for k, v := range m.raw {
		out[k] = v
	}
	return out
}

// EffectiveTimeout returns the hook timeout, falling back to the configured
// default when TIMEOUT is 0 (or unset).
func (m *Meta) EffectiveTimeout(def int) int {
	if m.Timeout > 0 {
		return m.Timeout
	}
	if def > 0 {
		return def
	}
	return 0
}

// EffectiveOnFail resolves the hook's failure policy against the phase policy.
func (m *Meta) EffectiveOnFail(phasePolicy string) string {
	if m.OnFail != "" {
		return m.OnFail
	}
	if phasePolicy == "" {
		return OnFailContinue
	}
	return phasePolicy
}

// Display returns the hook name as shown in tables ("10-storage-mount").
func (m *Meta) Display() string {
	if m.Name != "" {
		return m.Name
	}
	return strings.TrimSuffix(m.FileName, filepath.Ext(m.FileName))
}

// sortKey orders hooks: numeric filename prefix first, then filename.
func (m *Meta) sortKey() (int, string) {
	return m.Order, m.FileName
}

func less(a, b *Meta) bool {
	ao, an := a.sortKey()
	bo, bn := b.sortKey()
	if ao != bo {
		return ao < bo
	}
	return an < bn
}

// Error is a header parse/validation error with file and line context.
type Error struct {
	File  string
	Line  int
	Field string
	Msg   string
}

func (e *Error) Error() string {
	loc := e.File
	if e.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, e.Line)
	}
	if e.Field != "" {
		return fmt.Sprintf("%s: %s: %s", loc, e.Field, e.Msg)
	}
	return fmt.Sprintf("%s: %s", loc, e.Msg)
}

var (
	nameRe  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	fieldRe = regexp.MustCompile(`^\s*#\s*([A-Za-z][A-Za-z0-9_]*)\s*:\s*(.*?)\s*$`)
	varRe   = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)(:-([^}]*))?\}`)
)

// Parse parses the header of a hook. file is used for error messages and to
// derive the hook name when NAME is missing; data is the script content.
//
// The returned warnings are non-fatal issues (unknown or duplicate fields).
func Parse(file string, data []byte) (*Meta, []string, error) {
	var warnings []string

	if len(data) > MaxHeaderBytes {
		data = data[:MaxHeaderBytes]
	}
	lines := strings.Split(string(data), "\n")

	begin, end := -1, -1
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if begin < 0 {
			if l == headerBegin || strings.HasPrefix(l, headerBegin) && strings.Contains(l, headerBegin) {
				begin = i
				continue
			}
			// Stop scanning after the header region: sballd timeout would be
			// huge, but a hook without a header within the first 200 lines is
			// clearly misconfigured.
			if i > 200 {
				break
			}
			continue
		}
		if strings.Contains(l, headerEnd) {
			end = i
			break
		}
	}
	if begin < 0 {
		return nil, nil, &Error{File: file, Msg: fmt.Sprintf("missing header: expected %q", headerBegin)}
	}
	if end < 0 {
		return nil, nil, &Error{File: file, Line: begin + 1, Msg: fmt.Sprintf("unterminated header: expected %q", headerEnd)}
	}

	m := &Meta{
		Enabled:   true,
		Target:    TargetLocal,
		raw:       map[string]string{},
		lineIndex: map[string]int{},
	}
	m.Path = file
	m.FileName = filepath.Base(file)
	if n, ok := parseOrderPrefix(m.FileName); ok {
		m.Order, m.OrderSet = n, true
	}
	for i := begin + 1; i < end; i++ {
		raw := lines[i]
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || trimmed == "#" {
			continue
		}
		match := fieldRe.FindStringSubmatch(raw)
		if match == nil {
			// Non "# KEY: value" lines inside the header are comments.
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			return nil, warnings, &Error{File: file, Line: i + 1, Msg: fmt.Sprintf("unexpected content inside header: %q", trimmed)}
		}
		key := strings.ToUpper(match[1])
		value := expandVars(match[2])
		if _, dup := m.raw[key]; dup {
			warnings = append(warnings, fmt.Sprintf("%s:%d: duplicate field %s (last value wins)", file, i+1, key))
		}
		m.raw[key] = value
		m.lineIndex[key] = i

		if err := m.applyField(key, value, file, i+1); err != nil {
			return nil, warnings, err
		}
	}

	if err := m.validate(file, begin+1); err != nil {
		return nil, warnings, err
	}
	return m, warnings, nil
}

func (m *Meta) applyField(key, value, file string, line int) error {
	switch key {
	case "HOOK":
		p, err := ParsePhase(value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.Phase = p
	case "NAME":
		m.Name = value
	case "DESCRIPTION":
		m.Description = value
	case "TIMEOUT":
		n, err := parseIntField(key, value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.Timeout = n
	case "RETRY":
		n, err := parseIntField(key, value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.Retry = n
	case "RETRY_BACKOFF":
		n, err := parseIntField(key, value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.RetryBackoff = n
	case "ON_FAIL":
		v := strings.ToLower(strings.TrimSpace(value))
		if v != OnFailAbort && v != OnFailContinue {
			return &Error{File: file, Line: line, Field: key, Msg: fmt.Sprintf("%q is not abort|continue", value)}
		}
		m.OnFail = v
	case "RUN_ONCE":
		b, err := parseBoolField(key, value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.RunOnce = b
	case "AFTER":
		m.After = value
	case "TARGET":
		t := Target(strings.ToLower(strings.TrimSpace(value)))
		if !t.Valid() {
			return &Error{File: file, Line: line, Field: key, Msg: fmt.Sprintf("%q is not local|master|all", value)}
		}
		m.Target = t
	case "ENABLED":
		b, err := parseBoolField(key, value)
		if err != nil {
			return &Error{File: file, Line: line, Field: key, Msg: err.Error()}
		}
		m.Enabled = b
	default:
		// Unknown fields are tolerated so that future versions can add fields
		// without breaking older installations.
	}
	return nil
}

func (m *Meta) validate(file string, line int) error {
	if !m.Phase.Valid() {
		return &Error{File: file, Line: line, Field: "HOOK", Msg: "required field is missing"}
	}
	if m.Name == "" {
		return &Error{File: file, Line: line, Field: "NAME", Msg: "required field is missing"}
	}
	if !nameRe.MatchString(m.Name) {
		return &Error{File: file, Line: line, Field: "NAME", Msg: fmt.Sprintf("%q must match %s", m.Name, nameRe.String())}
	}
	if m.Timeout < 0 {
		return &Error{File: file, Line: line, Field: "TIMEOUT", Msg: "must be >= 0"}
	}
	if m.Retry < 0 {
		return &Error{File: file, Line: line, Field: "RETRY", Msg: "must be >= 0"}
	}
	if m.RetryBackoff < 0 {
		return &Error{File: file, Line: line, Field: "RETRY_BACKOFF", Msg: "must be >= 0"}
	}
	if m.After == m.Name {
		return &Error{File: file, Line: line, Field: "AFTER", Msg: "a hook cannot run after itself"}
	}
	return nil
}

func parseIntField(field, value string) (int, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return 0, fmt.Errorf("empty value is not a number")
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", value)
	}
	return n, nil
}

func parseBoolField(field, value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true", "yes", "on", "1", "enabled":
		return true, nil
	case "false", "no", "off", "0", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (true|false)", value)
	}
}

// expandVars expands ${VAR} and ${VAR:-default} using the process environment.
// This makes hooks portable across nodes without editing the script.
func expandVars(s string) string {
	if !strings.Contains(s, "${") {
		return s
	}
	return varRe.ReplaceAllStringFunc(s, func(match string) string {
		sub := varRe.FindStringSubmatch(match)
		name := sub[1]
		if v, ok := os.LookupEnv(name); ok && v != "" {
			return v
		}
		if sub[2] != "" { // had a :-default section
			return sub[3]
		}
		return ""
	})
}

var orderPrefixRe = regexp.MustCompile(`^([0-9]{1,5})[-_.]`)

func parseOrderPrefix(name string) (int, bool) {
	m := orderPrefixRe.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func stripHookSuffix(name string) string {
	for _, suffix := range []string{".sh", ".bash", ".hook"} {
		if strings.HasSuffix(name, suffix) {
			return strings.TrimSuffix(name, suffix)
		}
	}
	return name
}
