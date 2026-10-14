package hook

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Broken describes a file in a hook directory that could not be parsed.
type Broken struct {
	File string
	Err  error
}

// Set is the ordered collection of hooks found in one phase directory.
type Set struct {
	Phase    Phase
	Dir      string
	Hooks    []*Meta
	Broken   []Broken
	Warnings []string
	Errors   []error
	Missing  bool
}

// LoadPhase discovers and orders the hooks in dir for the given phase. Parse
// and ordering problems are reported through Set.Errors instead of being
// returned, so callers (list/status/validate) can still show partial results.
func LoadPhase(dir string, phase Phase) *Set {
	s := &Set{Phase: phase, Dir: dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			s.Missing = true
			s.Warnings = append(s.Warnings, fmt.Sprintf("hook directory %s does not exist", dir))
			return s
		}
		s.Errors = append(s.Errors, fmt.Errorf("read hook directory %s: %w", dir, err))
		return s
	}

	for _, entry := range entries {
		name := entry.Name()
		if !candidate(entry, name) {
			continue
		}
		path := filepath.Join(dir, name)
		info, err := os.Stat(path) // follows symlinks
		if err != nil {
			s.Broken = append(s.Broken, Broken{File: path, Err: err})
			continue
		}
		if info.IsDir() {
			continue
		}

		data, err := os.ReadFile(path)
		if err != nil {
			s.Broken = append(s.Broken, Broken{File: path, Err: fmt.Errorf("read: %w", err)})
			continue
		}

		meta, warns, err := Parse(path, data)
		if err != nil {
			s.Broken = append(s.Broken, Broken{File: path, Err: err})
			continue
		}
		s.Warnings = append(s.Warnings, warns...)

		meta.FileName = name
		if n, ok := parseOrderPrefix(name); ok {
			meta.Order, meta.OrderSet = n, true
		} else {
			meta.Order, meta.OrderSet = 0, false
		}
		meta.Executable = info.Mode().Perm()&0o111 != 0
		if !meta.Executable {
			s.Warnings = append(s.Warnings, fmt.Sprintf("%s is not executable; it will be skipped at run time (chmod +x)", path))
		}
		if meta.Phase != phase {
			warning := fmt.Sprintf("%s declares HOOK: %s but lives in %s", name, meta.Phase, filepath.Base(dir))
			s.Warnings = append(s.Warnings, warning)
		}
		s.Hooks = append(s.Hooks, meta)
	}

	// Best effort ordering first: even with broken hooks the table stays
	// readable.
	sort.SliceStable(s.Hooks, func(i, j int) bool { return less(s.Hooks[i], s.Hooks[j]) })

	if len(s.Broken) > 0 {
		for _, b := range s.Broken {
			s.Errors = append(s.Errors, b.Err)
		}
		return s
	}

	ordered, err := Sort(s.Hooks)
	if err != nil {
		s.Errors = append(s.Errors, err)
		return s
	}
	s.Hooks = ordered
	return s
}

// Find returns the hook with the given name.
func (s *Set) Find(name string) *Meta {
	for _, h := range s.Hooks {
		if h.Name == name || h.FileName == name {
			return h
		}
	}
	return nil
}

// Enabled returns only the hooks that are enabled in their header.
func (s *Set) Enabled() []*Meta {
	out := make([]*Meta, 0, len(s.Hooks))
	for _, h := range s.Hooks {
		if h.Enabled {
			out = append(out, h)
		}
	}
	return out
}

// Runnable reports whether the set is safe to execute (no parse or ordering
// errors).
func (s *Set) Runnable() bool { return len(s.Errors) == 0 }

// Names lists the hook names in execution order.
func (s *Set) Names() []string {
	out := make([]string, 0, len(s.Hooks))
	for _, h := range s.Hooks {
		out = append(out, h.Name)
	}
	return out
}

// LoadDir loads a phase set from an explicit directory.
func LoadDir(dir string, phase Phase) *Set { return LoadPhase(dir, phase) }

// candidate decides whether a directory entry is a hook file.
func candidate(entry fs.DirEntry, name string) bool {
	if entry.IsDir() {
		return false
	}
	if strings.HasPrefix(name, ".") {
		return false
	}
	lower := strings.ToLower(name)
	for _, suffix := range []string{"~", ".bak", ".orig", ".swp", ".swo", ".tmp", ".disabled", ".rpmnew", ".rpmsave", ".rpmorig"} {
		if strings.HasSuffix(lower, suffix) {
			return false
		}
	}
	if strings.HasPrefix(lower, "readme") {
		return false
	}
	// Everything else is a hook if it is a shell-ish script or executable.
	if strings.HasSuffix(lower, ".sh") || strings.HasSuffix(lower, ".bash") || strings.HasSuffix(lower, ".hook") {
		return true
	}
	info, err := entry.Info()
	if err != nil {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// FormatOrderName builds the canonical hook filename for a phase install:
// "10-register-monitoring.sh".
func FormatOrderName(order int, name string) string {
	base := stripHookSuffix(name)
	switch {
	case order <= 0:
		return base + ".sh"
	case order < 100:
		return fmt.Sprintf("%02d-%s.sh", order, base)
	default:
		return fmt.Sprintf("%03d-%s.sh", order, base)
	}
}
