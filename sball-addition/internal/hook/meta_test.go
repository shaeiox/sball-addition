package hook

import (
	"os"
	"strings"
	"testing"
)

const fullHeader = `#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: register-monitoring
# DESCRIPTION: Register node in monitoring system
# TIMEOUT: 60
# RETRY: 2
# RETRY_BACKOFF: 5
# ON_FAIL: continue
# RUN_ONCE: true
# AFTER: 10-network-check
# TARGET: master
# ENABLED: false
### END SBALL-ADDITION INFO
echo hello
`

func TestParseFullHeader(t *testing.T) {
	m, warns, err := Parse("/etc/hooks/post-quorate.d/30-register-monitoring.sh", []byte(fullHeader))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if m.Phase != PhasePost {
		t.Errorf("Phase = %q, want %q", m.Phase, PhasePost)
	}
	if m.Name != "register-monitoring" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Description != "Register node in monitoring system" {
		t.Errorf("Description = %q", m.Description)
	}
	if m.Timeout != 60 || m.Retry != 2 || m.RetryBackoff != 5 {
		t.Errorf("timeout/retry = %d/%d/%d", m.Timeout, m.Retry, m.RetryBackoff)
	}
	if m.OnFail != OnFailContinue {
		t.Errorf("OnFail = %q", m.OnFail)
	}
	if !m.RunOnce {
		t.Error("RunOnce should be true")
	}
	if m.After != "10-network-check" {
		t.Errorf("After = %q", m.After)
	}
	if m.Target != TargetMaster {
		t.Errorf("Target = %q", m.Target)
	}
	if m.Enabled {
		t.Error("Enabled should be false")
	}
	if !m.OrderSet || m.Order != 30 {
		t.Errorf("Order = %d (set=%v), want 30", m.Order, m.OrderSet)
	}
}

func TestParseDefaults(t *testing.T) {
	src := "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: minimal\n### END SBALL-ADDITION INFO\n"
	m, _, err := Parse("minimal.sh", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Timeout != 0 || m.Retry != 0 {
		t.Errorf("timeout/retry = %d/%d, want 0/0", m.Timeout, m.Retry)
	}
	if m.Target != TargetLocal {
		t.Errorf("Target = %q, want local", m.Target)
	}
	if !m.Enabled {
		t.Error("hooks must default to ENABLED: true")
	}
	if m.OnFail != "" {
		t.Errorf("OnFail = %q, want empty (inherit)", m.OnFail)
	}
	if m.Name != "minimal" {
		t.Errorf("Name = %q", m.Name)
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want string
	}{
		{"no header", "#!/bin/bash\necho hi\n", "missing header"},
		{"unterminated", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n", "unterminated header"},
		{"missing hook", "### BEGIN SBALL-ADDITION INFO\n# NAME: x\n### END SBALL-ADDITION INFO\n", "HOOK"},
		{"missing name", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n### END SBALL-ADDITION INFO\n", "NAME"},
		{"bad phase", "### BEGIN SBALL-ADDITION INFO\n# HOOK: during-quorate\n# NAME: x\n### END SBALL-ADDITION INFO\n", "invalid phase"},
		{"bad target", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n# TARGET: everywhere\n### END SBALL-ADDITION INFO\n", "local|master|all"},
		{"bad timeout", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n# TIMEOUT: soon\n### END SBALL-ADDITION INFO\n", "not a number"},
		{"bad bool", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n# RUN_ONCE: perhaps\n### END SBALL-ADDITION INFO\n", "not a boolean"},
		{"bad on_fail", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n# ON_FAIL: explode\n### END SBALL-ADDITION INFO\n", "abort|continue"},
		{"bad name", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: with space\n### END SBALL-ADDITION INFO\n", "must match"},
		{"self after", "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n# AFTER: x\n### END SBALL-ADDITION INFO\n", "cannot run after itself"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := Parse("/tmp/"+tc.name+".sh", []byte(tc.src))
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "/tmp/") {
				t.Errorf("error should mention the file: %v", err)
			}
		})
	}
}

func TestParseFieldVariants(t *testing.T) {
	src := `#!/bin/bash
### BEGIN SBALL-ADDITION INFO
# this is a comment
# HOOK:pre-quorate
#   NAME   :   spaced
# TIMEOUT: 10
# RETRY: 1
# FUTURE_FIELD: ignored
### END SBALL-ADDITION INFO
`
	m, warns, err := Parse("x.sh", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "spaced" {
		t.Errorf("Name = %q, want %q", m.Name, "spaced")
	}
	if m.Timeout != 10 {
		t.Errorf("Timeout = %d", m.Timeout)
	}
	if len(warns) != 0 {
		t.Errorf("unknown fields must be tolerated silently, got %v", warns)
	}
	if _, ok := m.Field("FUTURE_FIELD"); !ok {
		t.Error("raw fields should keep unknown values")
	}
}

func TestParseExpandsVariables(t *testing.T) {
	t.Setenv("SBALL_TEST_HOST", "spongebob")
	src := `### BEGIN SBALL-ADDITION INFO
# HOOK: post-quorate
# NAME: ${SBALL_TEST_HOST}-hook
# DESCRIPTION: runs on ${SBALL_TEST_HOST} in ${SBALL_TEST_MISSING:-production}
# AFTER: ${SBALL_TEST_UNSET}
### END SBALL-ADDITION INFO
`
	m, _, err := Parse("x.sh", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if m.Name != "spongebob-hook" {
		t.Errorf("Name = %q", m.Name)
	}
	if m.Description != "runs on spongebob in production" {
		t.Errorf("Description = %q", m.Description)
	}
	if m.After != "" {
		t.Errorf("After = %q, want empty", m.After)
	}
}

func TestDuplicateFieldWarns(t *testing.T) {
	src := `### BEGIN SBALL-ADDITION INFO
# HOOK: pre-quorate
# NAME: dup
# TIMEOUT: 10
# TIMEOUT: 20
### END SBALL-ADDITION INFO
`
	m, warns, err := Parse("x.sh", []byte(src))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "duplicate") {
		t.Fatalf("warnings = %v, want one duplicate warning", warns)
	}
	if m.Timeout != 20 {
		t.Errorf("last value should win, got %d", m.Timeout)
	}
}

func TestEffectiveTimeoutAndOnFail(t *testing.T) {
	m := &Meta{}
	if got := m.EffectiveTimeout(42); got != 42 {
		t.Errorf("EffectiveTimeout = %d, want the configured default 42", got)
	}
	m.Timeout = 7
	if got := m.EffectiveTimeout(42); got != 7 {
		t.Errorf("EffectiveTimeout = %d, want 7", got)
	}
	if got := m.EffectiveOnFail("abort"); got != "abort" {
		t.Errorf("EffectiveOnFail = %q, want the phase policy", got)
	}
	m.OnFail = "continue"
	if got := m.EffectiveOnFail("abort"); got != "continue" {
		t.Errorf("EffectiveOnFail = %q, want the hook policy", got)
	}
	if got := (&Meta{}).EffectiveOnFail(""); got != OnFailContinue {
		t.Errorf("EffectiveOnFail = %q, want continue as the last resort", got)
	}
}

func TestFormatOrderName(t *testing.T) {
	cases := map[string]string{}
	cases[FormatOrderName(10, "register-monitoring")] = "10-register-monitoring.sh"
	cases[FormatOrderName(0, "plain")] = "plain.sh"
	cases[FormatOrderName(120, "big")] = "120-big.sh"
	cases[FormatOrderName(5, "bold.sh")] = "05-bold.sh"
	for got, want := range cases {
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestSetEnabled(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/10-demo.sh"
	if err := os.WriteFile(path, []byte(fullHeader), 0o755); err != nil {
		t.Fatal(err)
	}
	// ENABLED: false -> true
	changed, err := SetEnabled(path, true)
	if err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	if !changed {
		t.Error("expected the file to change")
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# ENABLED: true") {
		t.Errorf("header not updated:\n%s", data)
	}
	if !strings.Contains(string(data), "echo hello") {
		t.Error("script body must be preserved")
	}
	// idempotent
	changed, err = SetEnabled(path, true)
	if err != nil || changed {
		t.Errorf("second call changed=%v err=%v, want false/nil", changed, err)
	}
	// and back
	if _, err := SetEnabled(path, false); err != nil {
		t.Fatalf("SetEnabled(false): %v", err)
	}
	m, _, err := Parse(path, mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled {
		t.Error("hook should be disabled again")
	}
}

func TestSetEnabledWithoutField(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/x.sh"
	src := "### BEGIN SBALL-ADDITION INFO\n# HOOK: pre-quorate\n# NAME: x\n### END SBALL-ADDITION INFO\n"
	if err := os.WriteFile(path, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := SetEnabled(path, false); err != nil {
		t.Fatalf("SetEnabled: %v", err)
	}
	m, _, err := Parse(path, mustRead(t, path))
	if err != nil {
		t.Fatal(err)
	}
	if m.Enabled {
		t.Error("the ENABLED field should have been added as false")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
