package cli

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/lock"
	"sball-addition/internal/proc"
	"sball-addition/internal/pvm"
	"sball-addition/internal/runner"
	"sball-addition/internal/version"
)

type phaseStatus struct {
	Dir     string `json:"dir"`
	Enabled bool   `json:"enabled"`
	Hooks   int    `json:"hooks"`
	On      int    `json:"enabled_hooks"`
}

type statusReport struct {
	Version         string          `json:"version"`
	Host            string          `json:"host"`
	Config          string          `json:"config"`
	LogFile         string          `json:"log_file"`
	Socket          string          `json:"socket"`
	SocketReachable bool            `json:"socket_reachable"`
	SballdPID       int             `json:"sballd_pid"`
	SballdUptime    string          `json:"sballd_uptime,omitempty"`
	SballdState     string          `json:"sballd_state"`
	PVMState        string          `json:"pvm_state"`
	Quorate         bool            `json:"quorate"`
	MasterID        string          `json:"master_id,omitempty"`
	MasterName      string          `json:"master_name,omitempty"`
	IsMaster        bool            `json:"is_master"`
	NodeCount       int             `json:"node_count"`
	WatcherRunning  bool            `json:"watcher_running"`
	WatcherPID      int             `json:"watcher_pid,omitempty"`
	WatcherUptime   string          `json:"watcher_uptime,omitempty"`
	Pre             phaseStatus     `json:"pre_quorate"`
	Post            phaseStatus     `json:"post_quorate"`
	LastPre         *runner.LastRun `json:"last_pre_hook,omitempty"`
	LastPost        *runner.LastRun `json:"last_post_hook,omitempty"`
}

func (a *App) cmdStatus(args []string) int {
	f := newFlagSet("status", args)
	asJSON := f.Bool("json", false, "machine readable output")
	if err := f.Parse(); err != nil {
		return a.usageError("status", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}

	rep := statusReport{
		Version: version.Version,
		Host:    pvm.LocalName(),
		Config:  cfg.Paths.ConfigFile,
		LogFile: cfg.Paths.LogFile,
		Socket:  fmt.Sprintf("%s:%d", cfg.General.SocketHost, cfg.General.SocketPort),
	}

	// --- sballd / cluster ------------------------------------------------
	rep.SballdPID = proc.Find("sballd")
	if rep.SballdPID > 0 {
		if d, ok := proc.Uptime(rep.SballdPID); ok {
			rep.SballdUptime = proc.FormatUptime(d)
		}
	}

	status, err := a.queryPVM(cfg)
	switch {
	case err == nil:
		rep.SocketReachable = true
		rep.MasterID = status.MasterID
		rep.IsMaster = status.IsMaster
		rep.NodeCount = len(status.Nodes)
		rep.Quorate = status.Quorate
		rep.PVMState = status.State
		if status.Local != nil {
			rep.PVMState = status.Local.PVMState
		}
		for _, n := range status.Nodes {
			if n.ID == status.MasterID {
				rep.MasterName = n.Name
				break
			}
		}
	case errors.Is(err, pvm.ErrUnavailable):
		rep.PVMState = "socket-unreachable"
	default:
		rep.PVMState = "error"
	}

	switch {
	case rep.SballdPID == 0:
		rep.SballdState = "DOWN"
	case rep.Quorate:
		rep.SballdState = "UP (quorate)"
	case rep.SocketReachable:
		rep.SballdState = fmt.Sprintf("UP (%s)", orDash(rep.PVMState))
	default:
		rep.SballdState = "UP (socket unreachable)"
	}

	// --- watcher ---------------------------------------------------------
	watcherPID := readPidFile(cfg.Paths.PidFile())
	if watcherPID > 0 && proc.Alive(watcherPID) {
		rep.WatcherRunning = true
		rep.WatcherPID = watcherPID
		if d, ok := proc.Uptime(watcherPID); ok {
			rep.WatcherUptime = proc.FormatUptime(d)
		}
	} else if holder := lockHolder(cfg); holder > 0 && proc.Alive(holder) {
		rep.WatcherRunning = true
		rep.WatcherPID = holder
	}

	// --- hooks -----------------------------------------------------------
	preSet := hook.LoadPhase(cfg.Paths.PreDir, hook.PhasePre)
	postSet := hook.LoadPhase(cfg.Paths.PostDir, hook.PhasePost)
	rep.Pre = phaseStatus{
		Dir:     preSet.Dir,
		Enabled: cfg.PreQuorate.Enabled,
		Hooks:   len(preSet.Hooks),
		On:      len(preSet.Enabled()),
	}
	rep.Post = phaseStatus{
		Dir:     postSet.Dir,
		Enabled: cfg.PostQuorate.Enabled,
		Hooks:   len(postSet.Hooks),
		On:      len(postSet.Enabled()),
	}

	lastRuns, _ := runner.ReadLastRuns(cfg.Paths.LastRunFile())
	rep.LastPre = pickLast(lastRuns, hook.PhasePre)
	rep.LastPost = pickLast(lastRuns, hook.PhasePost)

	if *asJSON {
		a.printJSON(rep)
		return ExitOK
	}
	a.renderStatus(rep, cfg, append(preSet.Errors, postSet.Errors...))
	return ExitOK
}

func (a *App) renderStatus(rep statusReport, cfg *config.Config, problems []error) {
	w := a.Stdout
	fmt.Fprintf(w, "%s status\n", version.Name)
	row := func(label, value string) {
		fmt.Fprintf(w, "  %-15s %s\n", label+":", value)
	}
	row("Version", rep.Version)
	row("Host", orDash(rep.Host))
	row("Config", rep.Config)
	row("Log file", rep.LogFile)
	socket := rep.Socket
	if rep.SocketReachable {
		socket += fmt.Sprintf(" (reachable, %d node(s))", rep.NodeCount)
	} else {
		socket += " (unreachable)"
	}
	row("Socket", socket)
	row("sballd PID", fmt.Sprintf("%d", rep.SballdPID))
	if rep.SballdUptime != "" {
		row("sballd Uptime", rep.SballdUptime)
	}
	row("sballd State", rep.SballdState)
	if rep.MasterID != "" {
		master := fmt.Sprintf("node %s", rep.MasterID)
		if rep.MasterName != "" {
			master += " (" + rep.MasterName + ")"
		}
		if rep.IsMaster {
			master += " [this node]"
		}
		row("Cluster Master", master)
	}
	row("Watcher", watcherLine(rep))
	row("Hooks", fmt.Sprintf("pre-quorate %d/%d enabled (%s), post-quorate %d/%d enabled (%s)",
		rep.Pre.On, rep.Pre.Hooks, onOff(rep.Pre.Enabled),
		rep.Post.On, rep.Post.Hooks, onOff(rep.Post.Enabled)))
	row("Last pre-hook", lastRunLine(rep.LastPre))
	row("Last post-hook", lastRunLine(rep.LastPost))

	if len(problems) > 0 {
		fmt.Fprintf(a.Stderr, "\n%s: %d hook problem(s):\n", version.Name, len(problems))
		for _, e := range problems {
			fmt.Fprintf(a.Stderr, "  - %v\n", e)
		}
	}
	_ = cfg
}

func watcherLine(rep statusReport) string {
	if !rep.WatcherRunning {
		return "not running"
	}
	line := fmt.Sprintf("running (pid %d", rep.WatcherPID)
	if rep.WatcherUptime != "" {
		line += ", uptime " + rep.WatcherUptime
	}
	return line + ")"
}

func lastRunLine(rec *runner.LastRun) string {
	if rec == nil {
		return "never"
	}
	word := map[string]string{"finish": "OK", "fail": "FAIL", "timeout": "TIMEOUT"}[rec.Event]
	if word == "" {
		word = strings.ToUpper(rec.Event)
	}
	return fmt.Sprintf("%s (%s, %s)", rec.Hook, word, relative(rec.At))
}

// pickLast returns the most recent record for a phase.
func pickLast(runs map[string]runner.LastRun, phase hook.Phase) *runner.LastRun {
	var best *runner.LastRun
	keys := make([]string, 0, len(runs))
	for k := range runs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		rec := runs[k]
		if rec.Phase != string(phase) {
			continue
		}
		r := rec
		if best == nil || r.At.After(best.At) {
			best = &r
		}
	}
	return best
}

// readPidFile reads a pid file, returning 0 when it is missing or malformed.
func readPidFile(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func lockHolder(cfg *config.Config) int {
	return lock.Holder(cfg.Paths.LockFile())
}
