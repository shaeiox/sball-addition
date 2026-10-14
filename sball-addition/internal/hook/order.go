package hook

import (
	"fmt"
	"sort"
	"strings"
)

// CycleError reports a circular AFTER dependency.
type CycleError struct {
	Phase Phase
	Hooks []string
}

func (e *CycleError) Error() string {
	return fmt.Sprintf("circular AFTER dependency in %s: %s", e.Phase, strings.Join(e.Hooks, " -> "))
}

// UnknownDependencyError reports an AFTER target that does not exist.
type UnknownDependencyError struct {
	Phase   Phase
	Hook    string
	Missing string
	Known   []string
}

func (e *UnknownDependencyError) Error() string {
	return fmt.Sprintf("hook %q (phase %s) declares AFTER: %s, but no such hook exists in this phase",
		e.Hook, e.Phase, e.Missing)
}

// DuplicateNameError reports two hooks sharing the same NAME in one phase.
type DuplicateNameError struct {
	Phase Phase
	Name  string
	Files []string
}

func (e *DuplicateNameError) Error() string {
	return fmt.Sprintf("duplicate hook name %q in phase %s: %s", e.Name, e.Phase, strings.Join(e.Files, ", "))
}

// CheckUnique verifies hook names are unique within the slice.
func CheckUnique(hooks []*Meta) error {
	seen := map[string][]string{}
	var order []string
	for _, h := range hooks {
		if _, ok := seen[h.Name]; !ok {
			order = append(order, h.Name)
		}
		seen[h.Name] = append(seen[h.Name], h.FileName)
	}
	for _, name := range order {
		if files := seen[name]; len(files) > 1 {
			phase := Phase("")
			for _, h := range hooks {
				if h.Name == name {
					phase = h.Phase
					break
				}
			}
			return &DuplicateNameError{Phase: phase, Name: name, Files: files}
		}
	}
	return nil
}

// Sort returns hooks in execution order:
//
//  1. numeric filename prefix, then filename (for "10-foo.sh" style names this
//     is identical to plain alphabetical order);
//  2. stable topological ordering driven by the AFTER field.
//
// Ties are broken deterministically by the filename sort key, so the result is
// stable across runs and nodes.
func Sort(hooks []*Meta) ([]*Meta, error) {
	if err := CheckUnique(hooks); err != nil {
		return nil, err
	}

	sorted := make([]*Meta, len(hooks))
	copy(sorted, hooks)
	sort.SliceStable(sorted, func(i, j int) bool { return less(sorted[i], sorted[j]) })

	byName := make(map[string]*Meta, len(sorted))
	for _, h := range sorted {
		byName[h.Name] = h
	}

	// in-degree and dependents for Kahn's algorithm.
	inDegree := make(map[string]int, len(sorted))
	dependents := map[string][]string{}
	for _, h := range sorted {
		if _, ok := inDegree[h.Name]; !ok {
			inDegree[h.Name] = 0
		}
		if h.After == "" {
			continue
		}
		target, ok := byName[h.After]
		if !ok {
			return nil, &UnknownDependencyError{
				Phase:   h.Phase,
				Hook:    h.Name,
				Missing: h.After,
				Known:   names(sorted),
			}
		}
		_ = target
		inDegree[h.Name]++
		dependents[h.After] = append(dependents[h.After], h.Name)
	}

	ready := make([]*Meta, 0, len(sorted))
	for _, h := range sorted {
		if inDegree[h.Name] == 0 {
			ready = append(ready, h)
		}
	}

	out := make([]*Meta, 0, len(sorted))
	for len(ready) > 0 {
		sort.SliceStable(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		next := ready[0]
		ready = ready[1:]
		out = append(out, next)
		for _, dep := range dependents[next.Name] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				for _, h := range sorted {
					if h.Name == dep {
						ready = append(ready, h)
						break
					}
				}
			}
		}
	}

	if len(out) != len(sorted) {
		remaining := make([]string, 0, len(sorted)-len(out))
		done := map[string]bool{}
		for _, h := range out {
			done[h.Name] = true
		}
		for _, h := range sorted {
			if !done[h.Name] {
				remaining = append(remaining, h.Name)
			}
		}
		return nil, &CycleError{Phase: sorted[0].Phase, Hooks: remaining}
	}
	return out, nil
}

func names(hooks []*Meta) []string {
	out := make([]string, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, h.Name)
	}
	return out
}
