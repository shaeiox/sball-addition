package cli

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"sball-addition/internal/config"
	"sball-addition/internal/hook"
	"sball-addition/internal/version"
)

// hookRow is one line of "sball-addition list" (and its --json form).
type hookRow struct {
	Phase        string `json:"phase"`
	Order        int    `json:"order"`
	OrderSet     bool   `json:"order_set"`
	Name         string `json:"name"`
	Enabled      bool   `json:"enabled"`
	Target       string `json:"target"`
	Description  string `json:"description"`
	Timeout      int    `json:"timeout"`
	Retry        int    `json:"retry"`
	RetryBackoff int    `json:"retry_backoff"`
	OnFail       string `json:"on_fail"`
	RunOnce      bool   `json:"run_once"`
	After        string `json:"after"`
	File         string `json:"file"`
	Executable   bool   `json:"executable"`
	Valid        bool   `json:"valid"`
}

func (a *App) cmdList(args []string) int {
	f := newFlagSet("list", args)
	phaseStr := f.String("phase", "", "restrict to one phase")
	asJSON := f.Bool("json", false, "machine readable output")
	strict := f.Bool("strict", false, "exit non-zero when a hook is invalid")
	if err := f.Parse(); err != nil {
		return a.usageError("list", err)
	}
	phases, err := selectPhases(*phaseStr)
	if err != nil {
		return a.usageError("list", err)
	}
	cfg, code := a.config()
	if cfg == nil {
		return code
	}

	rows, problems := a.collectRows(cfg, phases)
	for _, w := range collectWarnings(a.hooksFor(cfg, phases)) {
		fmt.Fprintf(a.Stderr, "%s: warning: %s\n", version.Name, w)
	}

	if *asJSON {
		if rows == nil {
			rows = []hookRow{}
		}
		a.printJSON(struct {
			Hooks    []hookRow `json:"hooks"`
			Problems []string  `json:"problems"`
		}{Hooks: rows, Problems: problems})
		if *strict && len(problems) > 0 {
			return ExitNotFound
		}
		return ExitOK
	}

	tw := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "PHASE\tORDER\tNAME\tENABLED\tTARGET\tDESCRIPTION")
	for _, r := range rows {
		order := "-"
		if r.OrderSet {
			order = fmt.Sprintf("%d", r.Order)
		}
		enabled := yesNo(r.Enabled)
		if !r.Valid {
			enabled = "INVALID"
		}
		desc := r.Description
		if desc == "" {
			desc = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Phase, order, r.Name, enabled, r.Target, desc)
	}
	_ = tw.Flush()

	if len(rows) == 0 {
		fmt.Fprintf(a.Stdout, "\nno hooks installed (looked in %s)\n", cfg.Paths.HooksDir)
	}
	if len(problems) > 0 {
		fmt.Fprintf(a.Stderr, "\n%s: %d hook problem(s):\n", version.Name, len(problems))
		for _, p := range problems {
			fmt.Fprintf(a.Stderr, "  - %s\n", p)
		}
		if *strict {
			return ExitNotFound
		}
	}
	return ExitOK
}

// collectRows loads the requested phases and flattens them into table rows.
func (a *App) collectRows(cfg *config.Config, phases []hook.Phase) ([]hookRow, []string) {
	var rows []hookRow
	var problems []string
	for _, phase := range phases {
		set := hook.LoadPhase(cfg.Paths.Dir(string(phase)), phase)
		for _, h := range set.Hooks {
			rows = append(rows, hookRow{
				Phase:        string(h.Phase),
				Order:        h.Order,
				OrderSet:     h.OrderSet,
				Name:         h.Name,
				Enabled:      h.Enabled,
				Target:       string(h.Target),
				Description:  h.Description,
				Timeout:      h.Timeout,
				Retry:        h.Retry,
				RetryBackoff: h.RetryBackoff,
				OnFail:       h.OnFail,
				RunOnce:      h.RunOnce,
				After:        h.After,
				File:         h.Path,
				Executable:   h.Executable,
				Valid:        true,
			})
		}
		for _, b := range set.Broken {
			rows = append(rows, hookRow{
				Phase:       string(phase),
				Name:        strings.TrimSuffix(b.File, ".sh"),
				Description: fmt.Sprintf("INVALID: %v", b.Err),
				File:        b.File,
				Valid:       false,
			})
		}
		for _, e := range set.Errors {
			problems = append(problems, e.Error())
		}
		for _, b := range set.Broken {
			problems = append(problems, b.Err.Error())
		}
	}
	return rows, dedupeStrings(problems)
}

func collectWarnings(sets []*hook.Set) []string {
	var out []string
	for _, s := range sets {
		out = append(out, s.Warnings...)
	}
	return dedupeStrings(out)
}

func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func selectPhases(s string) ([]hook.Phase, error) {
	if strings.TrimSpace(s) == "" {
		return hook.AllPhases(), nil
	}
	p, err := hook.ParsePhase(s)
	if err != nil {
		return nil, err
	}
	return []hook.Phase{p}, nil
}
