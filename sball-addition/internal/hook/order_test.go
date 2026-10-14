package hook

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mk(name, fileName, after string) *Meta {
	return &Meta{
		Phase:    PhasePost,
		Name:     name,
		FileName: fileName,
		After:    after,
		Enabled:  true,
		Target:   TargetLocal,
		Path:     "/hooks/" + fileName,
	}
}

func hookNames(hooks []*Meta) []string {
	out := make([]string, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, h.Name)
	}
	return out
}

func TestSortAlphabetical(t *testing.T) {
	in := []*Meta{
		mk("third", "30-c.sh", ""),
		mk("first", "10-a.sh", ""),
		mk("second", "20-b.sh", ""),
	}
	got, err := Sort(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"first", "second", "third"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("order = %v, want %v", hookNames(got), want)
		}
	}
}

func TestSortNumericPrefixBeatsStringOrder(t *testing.T) {
	// Plain alphabetical would put "10-x" before "9-y"; the numeric prefix
	// ordering keeps the admin's intent.
	in := []*Meta{
		withOrder(mk("x", "10-x.sh", "")),
		withOrder(mk("y", "9-y.sh", "")),
	}
	got, err := Sort(in)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "y" || got[1].Name != "x" {
		t.Fatalf("order = %v, want [y x] (9-y.sh before 10-x.sh)", hookNames(got))
	}
}

// withOrder derives the numeric prefix from the file name, like LoadPhase does.
func withOrder(h *Meta) *Meta {
	if n, ok := parseOrderPrefix(h.FileName); ok {
		h.Order, h.OrderSet = n, true
	}
	return h
}

func TestSortAfterDependency(t *testing.T) {
	in := []*Meta{
		mk("c", "30-c.sh", "b"),
		mk("a", "10-a.sh", ""),
		mk("b", "20-b.sh", "a"),
	}
	// break the alphabetical shortcut: rename files so the AFTER chain must
	// reorder them.
	in[0].FileName, in[0].Order = "10-c.sh", 10
	in[1].FileName, in[1].Order = "20-a.sh", 20
	in[2].FileName, in[2].Order = "30-b.sh", 30

	got, err := Sort(in)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"a", "b", "c"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("order = %v, want %v", hookNames(got), want)
		}
	}
}

func TestSortUnknownDependency(t *testing.T) {
	in := []*Meta{
		mk("a", "10-a.sh", "does-not-exist"),
	}
	_, err := Sort(in)
	var unknown *UnknownDependencyError
	if !errors.As(err, &unknown) {
		t.Fatalf("err = %v, want UnknownDependencyError", err)
	}
	if unknown.Missing != "does-not-exist" {
		t.Errorf("missing = %q", unknown.Missing)
	}
}

func TestSortCycle(t *testing.T) {
	in := []*Meta{
		mk("a", "10-a.sh", "b"),
		mk("b", "20-b.sh", "a"),
	}
	_, err := Sort(in)
	var cycle *CycleError
	if !errors.As(err, &cycle) {
		t.Fatalf("err = %v, want CycleError", err)
	}
	if len(cycle.Hooks) != 2 {
		t.Errorf("cycle hooks = %v", cycle.Hooks)
	}
}

func TestSortDuplicateName(t *testing.T) {
	in := []*Meta{
		mk("same", "10-a.sh", ""),
		mk("same", "20-b.sh", ""),
	}
	_, err := Sort(in)
	var dup *DuplicateNameError
	if !errors.As(err, &dup) {
		t.Fatalf("err = %v, want DuplicateNameError", err)
	}
	if !strings.Contains(err.Error(), "10-a.sh") || !strings.Contains(err.Error(), "20-b.sh") {
		t.Errorf("error should list both files: %v", err)
	}
}

func TestLoadPhase(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "10-alpha.sh", "pre-quorate", "alpha", "")
	writeHook(t, dir, "20-beta.sh", "pre-quorate", "beta", "alpha")
	writeHook(t, dir, "30-gamma.sh", "pre-quorate", "gamma", "")
	// a backup file that must be ignored
	if err := writeFile(dir+"/20-beta.sh.bak", "garbage"); err != nil {
		t.Fatal(err)
	}
	// a broken hook that must be reported
	if err := writeFile(dir+"/40-broken.sh", "#!/bin/bash\necho no header\n"); err != nil {
		t.Fatal(err)
	}

	set := LoadPhase(dir, PhasePre)
	if set.Missing {
		t.Fatal("set should not be marked missing")
	}
	if len(set.Hooks) != 3 {
		t.Fatalf("hooks = %v", set.Names())
	}
	if len(set.Broken) != 1 {
		t.Fatalf("broken = %v", set.Broken)
	}
	if len(set.Errors) == 0 {
		t.Fatal("broken hooks must produce errors")
	}
	if got := strings.Join(set.Names(), ","); got != "alpha,beta,gamma" {
		t.Fatalf("names = %s", got)
	}
	if h := set.Find("beta"); h == nil || h.After != "alpha" {
		t.Fatalf("Find(beta) = %+v", h)
	}
}

func TestLoadPhaseMissingDirectory(t *testing.T) {
	set := LoadPhase(t.TempDir()+"/nope", PhasePost)
	if !set.Missing {
		t.Error("Missing should be true")
	}
	if len(set.Errors) != 0 {
		t.Errorf("a missing directory is not an error, got %v", set.Errors)
	}
	if !set.Runnable() {
		t.Error("a missing directory should still be runnable (empty phase)")
	}
}

func TestCandidateFiltering(t *testing.T) {
	dir := t.TempDir()
	// ignored: dotfile, backup, README, disabled marker
	for _, name := range []string{".hidden", "10-a.sh.bak", "README.md", "20-b.sh.disabled", "30-c.sh~"} {
		if err := writeFile(dir+"/"+name, "x"); err != nil {
			t.Fatal(err)
		}
	}
	writeHook(t, dir, "40-real.sh", "pre-quorate", "real", "")
	set := LoadPhase(dir, PhasePre)
	if len(set.Hooks) != 1 || len(set.Broken) != 0 {
		t.Fatalf("hooks=%v broken=%v", set.Names(), set.Broken)
	}
}

func writeHook(t *testing.T, dir, fileName, phase, name, after string) {
	t.Helper()
	h := &Meta{Phase: Phase(phase), Name: name, After: after}
	src := "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: " + string(h.Phase) +
		"\n# NAME: " + name + "\n"
	if after != "" {
		src += "# AFTER: " + after + "\n"
	}
	src += "### END SBALL-ADDITION INFO\nexit 0\n"
	if err := writeFile(dir+"/"+fileName, src); err != nil {
		t.Fatal(err)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(filepath.Clean(path), []byte(content), 0o755)
}
