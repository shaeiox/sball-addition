package cli

import (
	"reflect"
	"testing"
)

func TestFlagForms(t *testing.T) {
	f := newFlagSet("test", []string{
		"--phase=post-quorate",
		"--hook", "10-storage-mount",
		"--limit=50",
		"--follow",
		"--pretty=false",
		"-f",
		"positional",
		"--",
		"--not-a-flag",
	})
	phase := f.String("phase", "", "")
	hookName := f.String("hook", "", "")
	limit := f.Int("limit", 0, "")
	follow := f.Bool("follow", false, "", "f")
	pretty := f.Bool("pretty", false, "")

	if err := f.Parse(); err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if *phase != "post-quorate" {
		t.Errorf("phase = %q", *phase)
	}
	if *hookName != "10-storage-mount" {
		t.Errorf("hook = %q", *hookName)
	}
	if *limit != 50 {
		t.Errorf("limit = %d", *limit)
	}
	if !*follow {
		t.Error("--follow should be true")
	}
	if *pretty {
		t.Error("--pretty=false should be false")
	}
	if got := f.Positional(); !reflect.DeepEqual(got, []string{"positional", "--not-a-flag"}) {
		t.Errorf("positional = %v", got)
	}
	if f.Arg(0) != "positional" || f.Arg(5) != "" {
		t.Errorf("Arg = %q / %q", f.Arg(0), f.Arg(5))
	}
}

func TestFlagErrors(t *testing.T) {
	cases := [][]string{
		{"--unknown"},
		{"--limit=abc"},
		{"--hook"},
		{"--follow=maybe"},
	}
	for _, args := range cases {
		f := newFlagSet("test", args)
		f.String("hook", "", "")
		f.Int("limit", 0, "")
		f.Bool("follow", false, "")
		if err := f.Parse(); err == nil {
			t.Errorf("args %v: expected an error", args)
		}
	}
}

func TestSplitFlag(t *testing.T) {
	cases := []struct {
		arg    string
		name   string
		value  string
		hasVal bool
	}{
		{"--root=/tmp/x", "--root", "/tmp/x", true},
		{"--root", "--root", "", false},
		{"-f", "-f", "", false},
		{"plain", "", "", false},
		{"-", "", "", false},
		{"--config=a=b", "--config", "a=b", true},
	}
	for _, tc := range cases {
		name, value, has := splitFlag(tc.arg)
		if name != tc.name || value != tc.value || has != tc.hasVal {
			t.Errorf("splitFlag(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tc.arg, name, value, has, tc.name, tc.value, tc.hasVal)
		}
	}
}

func TestExtractGlobals(t *testing.T) {
	a := &App{}
	rest, err := a.extractGlobals([]string{"list", "--root=/tmp/x", "--phase=pre-quorate", "--log-level", "debug"})
	if err != nil {
		t.Fatalf("extractGlobals: %v", err)
	}
	if a.Root != "/tmp/x" || a.LogLevel != "debug" {
		t.Errorf("globals = root %q level %q", a.Root, a.LogLevel)
	}
	if !reflect.DeepEqual(rest, []string{"list", "--phase=pre-quorate"}) {
		t.Errorf("rest = %v", rest)
	}
}

func TestSelectPhases(t *testing.T) {
	phases, err := selectPhases("")
	if err != nil || len(phases) != 2 {
		t.Fatalf("phases = %v err = %v", phases, err)
	}
	phases, err = selectPhases("pre-quorate")
	if err != nil || len(phases) != 1 || string(phases[0]) != "pre-quorate" {
		t.Fatalf("phases = %v err = %v", phases, err)
	}
	if _, err := selectPhases("lunch"); err == nil {
		t.Error("an unknown phase must be rejected")
	}
}

func TestParseSince(t *testing.T) {
	if _, err := parseSince("10m"); err != nil {
		t.Errorf("duration: %v", err)
	}
	if _, err := parseSince("2026-10-14T12:00:00Z"); err != nil {
		t.Errorf("RFC3339: %v", err)
	}
	if _, err := parseSince("2026-10-14 12:00:00"); err != nil {
		t.Errorf("timestamp: %v", err)
	}
	if _, err := parseSince("sometime"); err == nil {
		t.Error("garbage must be rejected")
	}
}
