// Package config loads and validates sball-addition.conf and resolves the
// filesystem layout used by the rest of the program.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Default paths, relative to the installation root ("/" for a real install).
const (
	DefaultEtcDir   = "/etc/sball-addition"
	DefaultVarLib   = "/var/lib/sball-addition"
	DefaultLogDir   = "/var/log/sball-addition"
	DefaultConfName = "sball-addition.conf"

	// DefaultSbinDir is where sballd and its wrapper live.
	DefaultSbinDir = "/usr/sbin"
	// DefaultBinDir is where the sball-addition binary is installed.
	DefaultBinDir = "/usr/bin"
)

// General holds the [general] section.
type General struct {
	LogLevel       string
	LogFile        string
	SocketHost     string
	SocketPort     int
	DefaultTimeout int
}

// PreQuorate holds the [pre-quorate] section.
type PreQuorate struct {
	Enabled         bool
	OnFailure       string // abort | continue
	ContinueOnEmpty bool
}

// PostQuorate holds the [post-quorate] section.
type PostQuorate struct {
	Enabled             bool
	PollIntervalSeconds int
	PollTimeoutSeconds  int
	OnFailure           string // abort | continue
}

// Paths is the resolved filesystem layout.
type Paths struct {
	Root       string
	EtcDir     string
	HooksDir   string
	PreDir     string
	PostDir    string
	LibDir     string
	StateDir   string
	RuntimeDir string
	LogDir     string
	LogFile    string
	ConfigFile string
}

// Config is the effective configuration: built-in defaults, overridden by the
// config file, overridden by environment variables and CLI flags.
type Config struct {
	// Source is the config file that was read ("" when defaults are in use).
	Source      string
	General     General
	PreQuorate  PreQuorate
	PostQuorate PostQuorate
	Paths       Paths
}

// Defaults returns the built-in configuration for the supplied installation
// root ("/" for a normal install, or a temp dir for tests/containers).
func Defaults(root string) *Config {
	c := &Config{}
	c.General = General{
		LogLevel:       "info",
		LogFile:        filepath.Join(DefaultLogDir, "sball-addition.log"),
		SocketHost:     "127.0.0.1",
		SocketPort:     7090,
		DefaultTimeout: 60,
	}
	c.PreQuorate = PreQuorate{
		Enabled:         true,
		OnFailure:       "abort",
		ContinueOnEmpty: true,
	}
	c.PostQuorate = PostQuorate{
		Enabled:             true,
		PollIntervalSeconds: 2,
		PollTimeoutSeconds:  300,
		OnFailure:           "continue",
	}
	c.Paths = defaultPaths(root, "")
	return c
}

func defaultPaths(root, logFile string) Paths {
	if root == "" {
		root = "/"
	}
	abs := func(p string) string {
		if root == "/" {
			return p
		}
		return filepath.Join(root, p)
	}
	etc := abs(DefaultEtcDir)
	if logFile == "" {
		logFile = filepath.Join(DefaultLogDir, "sball-addition.log")
	}
	return Paths{
		Root:       root,
		EtcDir:     etc,
		HooksDir:   filepath.Join(etc, "hooks.d"),
		PreDir:     filepath.Join(etc, "hooks.d", "pre-quorate.d"),
		PostDir:    filepath.Join(etc, "hooks.d", "post-quorate.d"),
		LibDir:     filepath.Join(etc, "lib"),
		StateDir:   abs(filepath.Join(DefaultVarLib, "state")),
		RuntimeDir: abs(filepath.Join(DefaultVarLib, "runtime")),
		LogDir:     abs(DefaultLogDir),
		LogFile:    abs(logFile),
		ConfigFile: filepath.Join(etc, DefaultConfName),
	}
}

// DefaultRoot returns the installation root: the SBALL_ADDITION_ROOT
// environment variable when set, otherwise "/".
func DefaultRoot() string {
	if r := strings.TrimSpace(os.Getenv("SBALL_ADDITION_ROOT")); r != "" {
		return r
	}
	return "/"
}

// DefaultConfigPath returns the config path for the given root.
func DefaultConfigPath(root string) string {
	return filepath.Join(rootOrSlash(root), DefaultEtcDir, DefaultConfName)
}

func rootOrSlash(root string) string {
	if strings.TrimSpace(root) == "" {
		return "/"
	}
	return root
}

// Dir returns the hook directory for a phase ("pre-quorate"/"post-quorate").
func (p Paths) Dir(phase string) string {
	switch phase {
	case "pre-quorate":
		return p.PreDir
	case "post-quorate":
		return p.PostDir
	default:
		return p.HooksDir
	}
}

// PhaseStateDir returns /var/lib/sball-addition/state/<phase>.
func (p Paths) PhaseStateDir(phase string) string {
	return filepath.Join(p.StateDir, phase)
}

// StateFile returns the RUN_ONCE marker path for a hook.
func (p Paths) StateFile(phase, name string) string {
	return filepath.Join(p.PhaseStateDir(phase), name+".state")
}

// LastRunFile returns the runtime file recording the most recent outcome of
// every hook (used by "sball-addition status").
func (p Paths) LastRunFile() string { return filepath.Join(p.RuntimeDir, "last-run.json") }

// PidFile returns the watcher pid file path.
func (p Paths) PidFile() string { return filepath.Join(p.RuntimeDir, "watcher.pid") }

// LockFile returns the watcher lock file path.
func (p Paths) LockFile() string { return filepath.Join(p.RuntimeDir, "watcher.lock") }

// ApplyEnv overlays SBALL_ADDITION_* environment variables. It is used by the
// wrapper and by the integration tests.
func (c *Config) ApplyEnv() {
	if v := strings.TrimSpace(os.Getenv("SBALL_ADDITION_LOG_LEVEL")); v != "" {
		c.General.LogLevel = v
	}
	if v := strings.TrimSpace(os.Getenv("SBALL_ADDITION_LOG_FILE")); v != "" {
		c.General.LogFile = c.abs(v)
	}
	if v := strings.TrimSpace(os.Getenv("SBALL_ADDITION_SOCKET_HOST")); v != "" {
		c.General.SocketHost = v
	}
	if v := strings.TrimSpace(os.Getenv("SBALL_ADDITION_SOCKET_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil {
			c.General.SocketPort = p
		}
	}
}

func (c *Config) abs(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Paths.Root, p)
}

// Load reads the config file at path. When the file does not exist the
// built-in defaults for root are returned with a nil error: an absent config
// file is not an error, it just means "all defaults".
func Load(path, root string) (*Config, []string, error) {
	c := Defaults(root)
	if path != "" {
		c.Paths.ConfigFile = path
	}
	var warnings []string

	data, err := os.ReadFile(c.Paths.ConfigFile)
	switch {
	case os.IsNotExist(err):
		c.ApplyEnv()
		c.Paths.LogFile = c.abspathForRoot(c.General.LogFile)
		return c, warnings, nil
	case err != nil:
		return nil, nil, fmt.Errorf("read config %s: %w", c.Paths.ConfigFile, err)
	}

	c.Source = c.Paths.ConfigFile
	section := ""
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := strings.TrimSpace(stripComment(sc.Text()))
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "[") {
			if !strings.HasSuffix(raw, "]") {
				return nil, warnings, fmt.Errorf("%s:%d: malformed section header %q", c.Source, lineNo, raw)
			}
			section = normalizeKey(strings.Trim(raw, "[]"))
			if !knownSection(section) {
				warnings = append(warnings, fmt.Sprintf("%s:%d: unknown section [%s] ignored", c.Source, lineNo, section))
			}
			continue
		}

		key, value, ok := splitKeyValue(raw)
		if !ok {
			return nil, warnings, fmt.Errorf("%s:%d: expected key = value, got %q", c.Source, lineNo, raw)
		}
		key = normalizeKey(key)
		value = unquote(value)

		if err := c.set(section, key, value); err != nil {
			return nil, warnings, fmt.Errorf("%s:%d: %w", c.Source, lineNo, err)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, warnings, fmt.Errorf("read config %s: %w", c.Source, err)
	}

	c.ApplyEnv()
	c.Paths.LogFile = c.abspathForRoot(c.General.LogFile)
	return c, warnings, nil
}

func (c *Config) abspathForRoot(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Paths.Root, p)
}

func knownSection(s string) bool {
	switch s {
	case "general", "pre_quorate", "post_quorate", "paths":
		return true
	}
	return false
}

func (c *Config) set(section, key, value string) error {
	switch section {
	case "", "general":
		switch key {
		case "log_level":
			c.General.LogLevel = value
		case "log_file":
			c.General.LogFile = value
		case "socket_host":
			c.General.SocketHost = value
		case "socket_port":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("general.socket_port: %q is not a number", value)
			}
			c.General.SocketPort = n
		case "default_timeout":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("general.default_timeout: %q is not a number", value)
			}
			c.General.DefaultTimeout = n
		default:
			return fmt.Errorf("unknown key %q in [general]", key)
		}
	case "pre_quorate":
		switch key {
		case "enabled":
			b, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("pre-quorate.enabled: %w", err)
			}
			c.PreQuorate.Enabled = b
		case "on_failure":
			c.PreQuorate.OnFailure = value
		case "continue_on_empty":
			b, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("pre-quorate.continue_on_empty: %w", err)
			}
			c.PreQuorate.ContinueOnEmpty = b
		default:
			return fmt.Errorf("unknown key %q in [pre-quorate]", key)
		}
	case "post_quorate":
		switch key {
		case "enabled":
			b, err := parseBool(value)
			if err != nil {
				return fmt.Errorf("post-quorate.enabled: %w", err)
			}
			c.PostQuorate.Enabled = b
		case "poll_interval_seconds":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("post-quorate.poll_interval_seconds: %q is not a number", value)
			}
			c.PostQuorate.PollIntervalSeconds = n
		case "poll_timeout_seconds":
			n, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("post-quorate.poll_timeout_seconds: %q is not a number", value)
			}
			c.PostQuorate.PollTimeoutSeconds = n
		case "on_failure":
			c.PostQuorate.OnFailure = value
		default:
			return fmt.Errorf("unknown key %q in [post-quorate]", key)
		}
	case "paths":
		// The [paths] section is optional and mainly useful for tests and
		// non-standard layouts.
		switch key {
		case "etc_dir":
			c.Paths.EtcDir = c.abs(value)
			c.Paths.HooksDir = filepath.Join(c.Paths.EtcDir, "hooks.d")
			c.Paths.PreDir = filepath.Join(c.Paths.HooksDir, "pre-quorate.d")
			c.Paths.PostDir = filepath.Join(c.Paths.HooksDir, "post-quorate.d")
			c.Paths.LibDir = filepath.Join(c.Paths.EtcDir, "lib")
		case "hooks_dir":
			c.Paths.HooksDir = c.abs(value)
			c.Paths.PreDir = filepath.Join(c.Paths.HooksDir, "pre-quorate.d")
			c.Paths.PostDir = filepath.Join(c.Paths.HooksDir, "post-quorate.d")
		case "state_dir":
			c.Paths.StateDir = c.abs(value)
		case "runtime_dir":
			c.Paths.RuntimeDir = c.abs(value)
		case "log_dir":
			c.Paths.LogDir = c.abs(value)
		default:
			return fmt.Errorf("unknown key %q in [paths]", key)
		}
	default:
		// Unknown sections were already reported as warnings.
	}
	return nil
}

// Validate checks the configuration for contradictory or out-of-range values.
func (c *Config) Validate() error {
	if _, err := parseLogLevel(c.General.LogLevel); err != nil {
		return fmt.Errorf("general.log_level: %w", err)
	}
	if c.General.SocketHost == "" {
		return fmt.Errorf("general.socket_host must not be empty")
	}
	if c.General.SocketPort < 1 || c.General.SocketPort > 65535 {
		return fmt.Errorf("general.socket_port: %d out of range 1-65535", c.General.SocketPort)
	}
	if c.General.DefaultTimeout < 0 {
		return fmt.Errorf("general.default_timeout must be >= 0")
	}
	if err := checkOnFailure("pre-quorate", c.PreQuorate.OnFailure); err != nil {
		return err
	}
	if err := checkOnFailure("post-quorate", c.PostQuorate.OnFailure); err != nil {
		return err
	}
	if c.PostQuorate.PollIntervalSeconds < 1 {
		return fmt.Errorf("post-quorate.poll_interval_seconds must be >= 1")
	}
	if c.PostQuorate.PollTimeoutSeconds < 1 {
		return fmt.Errorf("post-quorate.poll_timeout_seconds must be >= 1")
	}
	return nil
}

func checkOnFailure(phase, v string) error {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "abort", "continue":
		return nil
	default:
		return fmt.Errorf("%s.on_failure: %q is not abort|continue", phase, v)
	}
}

func parseLogLevel(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "info", "warn", "warning", "error", "err":
		return strings.ToLower(strings.TrimSpace(s)), nil
	default:
		return "", fmt.Errorf("invalid level %q (expected debug|info|warn|error)", s)
	}
}

// NormalizeOnFailure lowercases and validates an on-failure policy, falling
// back to def when the value is empty.
func NormalizeOnFailure(v, def string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "abort", "continue":
		return v
	case "":
		return def
	default:
		return def
	}
}

func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "on", "1", "enabled":
		return true, nil
	case "false", "no", "off", "0", "disabled":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (true|false)", s)
	}
}

func normalizeKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), "-", "_")
}

func splitKeyValue(line string) (string, string, bool) {
	i := strings.IndexAny(line, "=:")
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

func stripComment(line string) string {
	if i := strings.IndexAny(line, "#;"); i >= 0 {
		// Only treat it as a comment when it starts the line or follows
		// whitespace, so values containing '#' survive.
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i]
		}
	}
	return line
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
