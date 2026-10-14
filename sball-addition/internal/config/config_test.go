package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaults(t *testing.T) {
	c := Defaults("/")
	if c.General.LogLevel != "info" {
		t.Errorf("log level = %q", c.General.LogLevel)
	}
	if c.General.LogFile != "/var/log/sball-addition/sball-addition.log" {
		t.Errorf("log file = %q", c.General.LogFile)
	}
	if c.General.SocketHost != "127.0.0.1" || c.General.SocketPort != 7090 {
		t.Errorf("socket = %s:%d", c.General.SocketHost, c.General.SocketPort)
	}
	if c.General.DefaultTimeout != 60 {
		t.Errorf("default timeout = %d", c.General.DefaultTimeout)
	}
	if !c.PreQuorate.Enabled || c.PreQuorate.OnFailure != "abort" || !c.PreQuorate.ContinueOnEmpty {
		t.Errorf("pre-quorate defaults = %+v", c.PreQuorate)
	}
	if !c.PostQuorate.Enabled || c.PostQuorate.PollIntervalSeconds != 2 ||
		c.PostQuorate.PollTimeoutSeconds != 300 || c.PostQuorate.OnFailure != "continue" {
		t.Errorf("post-quorate defaults = %+v", c.PostQuorate)
	}
	if c.Paths.PreDir != "/etc/sball-addition/hooks.d/pre-quorate.d" {
		t.Errorf("pre dir = %q", c.Paths.PreDir)
	}
	if c.Paths.StateFile("post-quorate", "x") != "/var/lib/sball-addition/state/post-quorate/x.state" {
		t.Errorf("state file = %q", c.Paths.StateFile("post-quorate", "x"))
	}
}

func TestDefaultsWithRoot(t *testing.T) {
	c := Defaults("/tmp/sandbox")
	if c.Paths.PreDir != "/tmp/sandbox/etc/sball-addition/hooks.d/pre-quorate.d" {
		t.Errorf("pre dir = %q", c.Paths.PreDir)
	}
	if c.Paths.LogFile != "/tmp/sandbox/var/log/sball-addition/sball-addition.log" {
		t.Errorf("log file = %q", c.Paths.LogFile)
	}
}

func TestLoadFullConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sball-addition.conf")
	content := `# comment
[general]
log_level = debug        ; inline comment
log_file = /tmp/other.log
socket_host = 10.0.0.1
socket_port = 7091
default_timeout = 15

[pre-quorate]
enabled = false
on_failure = continue
continue_on_empty = false

[post-quorate]
enabled = true
poll_interval_seconds = 5
poll_timeout_seconds = 60
on_failure = abort
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, warns, err := Load(path, "/")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings = %v", warns)
	}
	if c.Source != path {
		t.Errorf("Source = %q", c.Source)
	}
	if c.General.LogLevel != "debug" || c.General.LogFile != "/tmp/other.log" {
		t.Errorf("general = %+v", c.General)
	}
	if c.Paths.LogFile != "/tmp/other.log" {
		t.Errorf("log file path = %q", c.Paths.LogFile)
	}
	if c.General.SocketPort != 7091 || c.General.DefaultTimeout != 15 {
		t.Errorf("general = %+v", c.General)
	}
	if c.PreQuorate.Enabled || c.PreQuorate.OnFailure != "continue" || c.PreQuorate.ContinueOnEmpty {
		t.Errorf("pre-quorate = %+v", c.PreQuorate)
	}
	if c.PostQuorate.PollIntervalSeconds != 5 || c.PostQuorate.OnFailure != "abort" {
		t.Errorf("post-quorate = %+v", c.PostQuorate)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLoadMissingFileUsesDefaults(t *testing.T) {
	c, _, err := Load(filepath.Join(t.TempDir(), "nope.conf"), "/")
	if err != nil {
		t.Fatalf("a missing config file must not be an error: %v", err)
	}
	if c.Source != "" {
		t.Errorf("Source = %q, want empty", c.Source)
	}
	if c.General.SocketPort != 7090 {
		t.Errorf("defaults not applied: %+v", c.General)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := map[string]string{
		"bad port":      "[general]\nsocket_port = http\n",
		"bad timeout":   "[general]\ndefault_timeout = later\n",
		"bad bool":      "[pre-quorate]\nenabled = maybe\n",
		"unknown key":   "[general]\nnope = 1\n",
		"bad section":   "[general]\nlog_level = info\n[general\n",
		"missing value": "[general]\nlog_level\n",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "c.conf")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Load(path, "/"); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestValidate(t *testing.T) {
	c := Defaults("/")
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
	c.General.SocketPort = 70000
	if err := c.Validate(); err == nil {
		t.Error("out of range port must fail")
	}
	c = Defaults("/")
	c.PostQuorate.PollIntervalSeconds = 0
	if err := c.Validate(); err == nil {
		t.Error("zero poll interval must fail")
	}
	c = Defaults("/")
	c.PreQuorate.OnFailure = "explode"
	if err := c.Validate(); err == nil {
		t.Error("unknown on_failure must fail")
	}
	c = Defaults("/")
	c.General.LogLevel = "loud"
	if err := c.Validate(); err == nil {
		t.Error("unknown log level must fail")
	}
}

func TestPathsSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.conf")
	content := `[paths]
hooks_dir = /srv/hooks
state_dir = /srv/state
runtime_dir = /srv/runtime
log_dir = /srv/log
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	c, _, err := Load(path, "/")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Paths.PreDir != "/srv/hooks/pre-quorate.d" {
		t.Errorf("pre dir = %q", c.Paths.PreDir)
	}
	if c.Paths.StateDir != "/srv/state" || c.Paths.RuntimeDir != "/srv/runtime" {
		t.Errorf("paths = %+v", c.Paths)
	}
}

func TestEnvOverrides(t *testing.T) {
	t.Setenv("SBALL_ADDITION_LOG_LEVEL", "warn")
	t.Setenv("SBALL_ADDITION_SOCKET_PORT", "7999")
	t.Setenv("SBALL_ADDITION_LOG_FILE", "/tmp/env.log")

	c, _, err := Load(filepath.Join(t.TempDir(), "missing.conf"), "/")
	if err != nil {
		t.Fatal(err)
	}
	if c.General.LogLevel != "warn" {
		t.Errorf("log level = %q", c.General.LogLevel)
	}
	if c.General.SocketPort != 7999 {
		t.Errorf("port = %d", c.General.SocketPort)
	}
	if c.Paths.LogFile != "/tmp/env.log" {
		t.Errorf("log file = %q", c.Paths.LogFile)
	}
}

func TestNormalizeOnFailure(t *testing.T) {
	if got := NormalizeOnFailure(" ABORT ", "continue"); got != "abort" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeOnFailure("", "abort"); got != "abort" {
		t.Errorf("got %q", got)
	}
	if got := NormalizeOnFailure("nonsense", "continue"); got != "continue" {
		t.Errorf("got %q", got)
	}
}

func TestRelativeLogFileIsAnchoredAtRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.conf")
	if err := os.WriteFile(path, []byte("[general]\nlog_file = var/log/custom.log\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c, _, err := Load(path, root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(c.Paths.LogFile, root) {
		t.Errorf("log file %q should live under the root %q", c.Paths.LogFile, root)
	}
}
