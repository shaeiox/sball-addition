package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sball-addition/internal/hook"
	"sball-addition/internal/log"
	"sball-addition/internal/pvm"
	"sball-addition/internal/version"
)

// cmdToggle implements "enable" and "disable": the hook header is the single
// source of truth, so the change survives config management and is visible in
// the hook file itself.
func (a *App) cmdToggle(args []string, enable bool) int {
	command := "disable"
	if enable {
		command = "enable"
	}
	f := newFlagSet(command, args)
	phaseStr := f.String("phase", "", "restrict to one phase")
	if err := f.Parse(); err != nil {
		return a.usageError(command, err)
	}
	target := f.Arg(0)
	if target == "" {
		return a.usageError(command, fmt.Errorf("missing <hook-name>"))
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}
	phases, err := selectPhases(*phaseStr)
	if err != nil {
		return a.usageError(command, err)
	}

	var matches []*hook.Meta
	var names []string
	for _, phase := range phases {
		set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
		if h := set.Find(target); h != nil {
			matches = append(matches, h)
		}
		names = append(names, set.Names()...)
	}
	if len(matches) == 0 {
		fmt.Fprintf(a.Stderr, "%s: hook %q not found\n", version.Name, target)
		if len(names) > 0 {
			sort.Strings(names)
			fmt.Fprintf(a.Stderr, "installed hooks: %s\n", strings.Join(names, ", "))
		}
		return ExitNotFound
	}

	rc := ExitOK
	for _, h := range matches {
		changed, err := hook.SetEnabled(h.Path, enable)
		if err != nil {
			fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
			lg.Error("hook_"+command+"_failed",
				log.String("phase", string(h.Phase)),
				log.String("hook", h.Name),
				log.String("file", h.Path),
				log.String("error", err.Error()),
			)
			rc = ExitFailure
			continue
		}
		event := "hook_disabled"
		state := "disabled"
		if enable {
			event, state = "hook_enabled", "enabled"
		}
		detail := " (already " + state + ")"
		if changed {
			detail = " (header updated)"
		}
		fmt.Fprintf(a.Stdout, "%s/%s: %s%s\n", h.Phase, h.Name, state, detail)
		lg.Info(event,
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.String("file", h.Path),
			log.Bool("changed", changed),
		)
	}
	return rc
}

func (a *App) cmdInstallHook(args []string) int {
	f := newFlagSet("install-hook", args)
	phaseStr := f.String("phase", "", "target phase (required)")
	order := f.Int("order", 0, "numeric filename prefix (e.g. 30)")
	force := f.Bool("force", false, "overwrite an existing hook with the same name")
	if err := f.Parse(); err != nil {
		return a.usageError("install-hook", err)
	}
	src := f.Arg(0)
	if src == "" {
		return a.usageError("install-hook", fmt.Errorf("missing <file>"))
	}
	if *phaseStr == "" {
		return a.usageError("install-hook", fmt.Errorf("--phase=pre-quorate|post-quorate is required"))
	}
	phase, err := hook.ParsePhase(*phaseStr)
	if err != nil {
		return a.usageError("install-hook", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}

	data, err := os.ReadFile(src)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
	meta, warnings, err := hook.Parse(src, data)
	if err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitNotFound
	}
	for _, w := range warnings {
		fmt.Fprintf(a.Stderr, "%s: warning: %s\n", version.Name, w)
	}
	if meta.Phase != phase {
		fmt.Fprintf(a.Stderr, "%s: %s declares HOOK: %s but --phase=%s was given; edit the header first\n",
			version.Name, src, meta.Phase, phase)
		return ExitUsage
	}

	destName := filepath.Base(src)
	if *order > 0 {
		destName = hook.FormatOrderName(*order, meta.Name)
	} else if !meta.OrderSet {
		destName = hook.FormatOrderName(0, meta.Name)
	}
	if !strings.HasSuffix(destName, ".sh") {
		destName += ".sh"
	}
	dest := filepath.Join(cfg.Paths.Dir(string(phase)), destName)

	if _, err := os.Stat(dest); err == nil && !*force {
		fmt.Fprintf(a.Stderr, "%s: %s already exists (use --force to overwrite)\n", version.Name, dest)
		return ExitFailure
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
	if err := os.WriteFile(dest, data, 0o755); err != nil {
		fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
		return ExitFailure
	}
	// Ensure the script is executable even when the umask interfered.
	if err := os.Chmod(dest, 0o755); err != nil {
		fmt.Fprintf(a.Stderr, "%s: warning: chmod %s: %v\n", version.Name, dest, err)
	}
	fmt.Fprintf(a.Stdout, "installed %s → %s\n", meta.Name, dest)
	lg.Info("hook_installed",
		log.String("phase", string(phase)),
		log.String("hook", meta.Name),
		log.String("file", dest),
		log.String("source", src),
	)
	return ExitOK
}

func (a *App) cmdUninstallHook(args []string) int {
	f := newFlagSet("uninstall-hook", args)
	phaseStr := f.String("phase", "", "restrict to one phase")
	keepState := f.Bool("keep-state", false, "keep the RUN_ONCE state file")
	if err := f.Parse(); err != nil {
		return a.usageError("uninstall-hook", err)
	}
	target := f.Arg(0)
	if target == "" {
		return a.usageError("uninstall-hook", fmt.Errorf("missing <hook-name>"))
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}
	lg, code := a.logger(cfg)
	if lg == nil {
		return code
	}
	phases, err := selectPhases(*phaseStr)
	if err != nil {
		return a.usageError("uninstall-hook", err)
	}

	removedAny := false
	rc := ExitOK
	for _, phase := range phases {
		set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
		h := set.Find(target)
		if h == nil {
			continue
		}
		removedAny = true
		if err := os.Remove(h.Path); err != nil {
			fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
			rc = ExitFailure
			continue
		}
		fmt.Fprintf(a.Stdout, "removed %s\n", h.Path)
		if !*keepState {
			statePath := cfg.Paths.StateFile(string(h.Phase), h.Name)
			if removed, err := removeIfExists(statePath); err != nil {
				fmt.Fprintf(a.Stderr, "%s: %v\n", version.Name, err)
				rc = ExitFailure
			} else if removed {
				fmt.Fprintf(a.Stdout, "removed %s\n", statePath)
			}
		}
		lg.Info("hook_uninstalled",
			log.String("phase", string(h.Phase)),
			log.String("hook", h.Name),
			log.String("file", h.Path),
			log.Bool("state_kept", *keepState),
		)
	}
	if !removedAny {
		fmt.Fprintf(a.Stderr, "%s: hook %q not found\n", version.Name, target)
		return ExitNotFound
	}
	return rc
}

func removeIfExists(path string) (bool, error) {
	err := os.Remove(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// validateResult is one check performed by "sball-addition validate".
type validateResult struct {
	Name    string `json:"check"`
	Status  string `json:"status"` // ok | warn | fail
	Message string `json:"message"`
}

func (a *App) cmdValidate(args []string) int {
	f := newFlagSet("validate", args)
	phaseStr := f.String("phase", "", "restrict hook checks to one phase")
	asJSON := f.Bool("json", false, "machine readable output")
	if err := f.Parse(); err != nil {
		return a.usageError("validate", err)
	}
	phases, err := selectPhases(*phaseStr)
	if err != nil {
		return a.usageError("validate", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}

	var results []validateResult
	add := func(status, name, format string, args ...any) {
		results = append(results, validateResult{Name: name, Status: status, Message: fmt.Sprintf(format, args...)})
	}

	// configuration file
	if cfg.Source == "" {
		add("warn", "config", "no config file at %s, built-in defaults are in use", cfg.Paths.ConfigFile)
	} else {
		add("ok", "config", "loaded %s", cfg.Source)
	}
	if err := cfg.Validate(); err != nil {
		add("fail", "config-values", "%v", err)
	} else {
		add("ok", "config-values", "log level %s, socket %s:%d, timeout %ds",
			cfg.General.LogLevel, cfg.General.SocketHost, cfg.General.SocketPort, cfg.General.DefaultTimeout)
	}

	// directories
	for _, dir := range []string{cfg.Paths.EtcDir, cfg.Paths.HooksDir, cfg.Paths.StateDir, cfg.Paths.RuntimeDir, cfg.Paths.LogDir} {
		if _, err := os.Stat(dir); err != nil {
			add("warn", "dir", "%s is missing (it will be created on demand)", dir)
		}
	}
	if writable, err := dirWritable(cfg.Paths.LogDir); err != nil {
		add("warn", "log-dir", "%v", err)
	} else if !writable {
		add("fail", "log-dir", "%s is not writable by uid %d", cfg.Paths.LogDir, os.Getuid())
	} else {
		add("ok", "log-dir", "%s is writable", cfg.Paths.LogDir)
	}

	// hooks
	for _, phase := range phases {
		set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
		if set.Missing {
			add("warn", "hooks-"+string(phase), "directory %s does not exist", set.Dir)
		} else if len(set.Errors) == 0 {
			add("ok", "hooks-"+string(phase), "%d hook(s), %d enabled in %s",
				len(set.Hooks), len(set.Enabled()), set.Dir)
		}
		for _, w := range set.Warnings {
			add("warn", "hook", "%s", w)
		}
		for _, b := range set.Broken {
			add("fail", "hook", "%v", b.Err)
		}
		for _, e := range set.Errors {
			add("fail", "hook-order", "%v", e)
		}
	}

	// install sanity
	sballdPath := filepath.Join(cfg.Paths.Root, "usr/sbin/sballd")
	if cfg.Paths.Root == "/" {
		sballdPath = "/usr/sbin/sballd"
	}
	if _, err := os.Stat(sballdPath); err != nil {
		add("warn", "sballd", "%s not found (set SBALLD_BIN for the wrapper)", sballdPath)
	} else {
		add("ok", "sballd", "found %s", sballdPath)
	}
	wrapper := "/usr/bin/sballd-wrapper"
	if _, err := os.Stat(wrapper); err != nil {
		add("warn", "wrapper", "%s not installed yet", wrapper)
	} else {
		add("ok", "wrapper", "found %s", wrapper)
	}

	// socket (informational: sballd is often down on purpose)
	client := pvm.New(cfg.General.SocketHost, cfg.General.SocketPort)
	if err := client.Ping(); err != nil {
		add("warn", "socket", "%s is not reachable (normal while sballd is down): %v", client.Addr(), err)
	} else if status, err := client.Status(); err != nil {
		add("warn", "socket", "%s is reachable but status lookup failed: %v", client.Addr(), err)
	} else {
		state := status.State
		if state == "" {
			state = "unknown"
		}
		add("ok", "socket", "%s reports %s (%d node(s), master %s)",
			client.Addr(), state, len(status.Nodes), orDash(status.MasterID))
	}

	failures, warnings := 0, 0
	for _, r := range results {
		switch r.Status {
		case "fail":
			failures++
		case "warn":
			warnings++
		}
	}

	if *asJSON {
		a.printJSON(struct {
			OK       bool             `json:"ok"`
			Failures int              `json:"failures"`
			Warnings int              `json:"warnings"`
			Checks   []validateResult `json:"checks"`
		}{OK: failures == 0, Failures: failures, Warnings: warnings, Checks: results})
	} else {
		for _, r := range results {
			marker := map[string]string{"ok": "ok  ", "warn": "warn", "fail": "FAIL"}[r.Status]
			out := a.Stdout
			if r.Status == "fail" {
				out = a.Stderr
			}
			fmt.Fprintf(out, "[%s] %-18s %s\n", marker, r.Name, r.Message)
		}
		fmt.Fprintf(a.Stdout, "\n%s: %d check(s), %d failure(s), %d warning(s)\n",
			version.Name, len(results), failures, warnings)
	}
	if failures > 0 {
		return ExitNotFound
	}
	return ExitOK
}

func dirWritable(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return false, fmt.Errorf("%s does not exist", dir)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%s is not a directory", dir)
	}
	probe := filepath.Join(dir, ".sball-addition-write-test")
	fh, err := os.OpenFile(probe, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return false, nil
	}
	_ = fh.Close()
	_ = os.Remove(probe)
	return true, nil
}
