// Package metrics renders sball-addition state in the Prometheus text
// exposition format, so a node's hook health can be scraped like any other
// service.
//
// The exporter is deliberately read-only: it inspects the last-run registry,
// the JSON log window and the PVM socket and never executes a hook. That makes
// it safe to run from a systemd timer every minute (see
// packaging/systemd/sball-addition-metrics.timer) or directly from an
// exporter, e.g. with node_exporter's textfile collector:
//
//	sball-addition metrics --out /var/lib/node_exporter/textfile_collector/sball-addition.prom
package metrics

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sample is one time series.
type Sample struct {
	Name   string
	Labels []Label
	Value  float64
}

// Label is a key/value pair attached to a sample.
type Label struct {
	Key   string
	Value string
}

// Metric is a set of samples that share a name, help text and type.
type Metric struct {
	Name    string
	Help    string
	Type    string // counter | gauge | summary
	Samples []Sample
}

// L builds a label.
func L(key, value string) Label { return Label{Key: key, Value: value} }

// Add appends a sample to the metric.
func (m *Metric) Add(value float64, labels ...Label) *Metric {
	m.Samples = append(m.Samples, Sample{Name: m.Name, Labels: labels, Value: value})
	return m
}

// Render writes the metrics in the Prometheus text exposition format.
//
// Samples are output sorted by metric name and then by their label set, which
// keeps the output stable between runs (nice for diffing and for tests).
func Render(w io.Writer, ms []*Metric) error {
	sorted := make([]*Metric, len(ms))
	copy(sorted, ms)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	bw := &errWriter{w: w}
	for _, m := range sorted {
		if m == nil || len(m.Samples) == 0 {
			continue
		}
		if m.Help != "" {
			bw.printf("# HELP %s %s\n", m.Name, escapeHelp(m.Help))
		}
		if m.Type != "" {
			bw.printf("# TYPE %s %s\n", m.Name, m.Type)
		}
		samples := make([]Sample, len(m.Samples))
		copy(samples, m.Samples)
		sort.SliceStable(samples, func(i, j int) bool {
			return labelKey(samples[i].Labels) < labelKey(samples[j].Labels)
		})
		for _, s := range samples {
			name := s.Name
			if name == "" {
				name = m.Name
			}
			bw.printf("%s%s %s\n", name, renderLabels(s.Labels), formatValue(s.Value))
		}
	}
	return bw.err
}

type errWriter struct {
	w   io.Writer
	err error
}

func (e *errWriter) printf(format string, args ...any) {
	if e.err != nil {
		return
	}
	if _, err := fmt.Fprintf(e.w, format, args...); err != nil {
		e.err = err
	}
}

func labelKey(labels []Label) string {
	parts := make([]string, 0, len(labels))
	for _, l := range labels {
		parts = append(parts, l.Key+"="+l.Value)
	}
	return strings.Join(parts, ",")
}

func renderLabels(labels []Label) string {
	if len(labels) == 0 {
		return ""
	}
	sorted := make([]Label, len(labels))
	copy(sorted, labels)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Key < sorted[j].Key })

	var sb strings.Builder
	sb.WriteByte('{')
	for i, l := range sorted {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(l.Key)
		sb.WriteString(`="`)
		sb.WriteString(escapeLabel(l.Value))
		sb.WriteString(`"`)
	}
	sb.WriteByte('}')
	return sb.String()
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return v
}

func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

func formatValue(v float64) string {
	if v == float64(int64(v)) && v < 1e15 && v > -1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// Deps is the data the collector needs. All fields are optional; missing files
// simply mean "no samples", never an error.
type Deps struct {
	Version     string
	Host        string
	ConfigFile  string
	LogFile     string
	LastRuns    map[string]LastRun
	LogLines    []string
	Pre         PhaseInfo
	Post        PhaseInfo
	SballdPID   int
	WatcherPID  int
	SocketOK    bool
	Quorate     bool
	MasterKnown bool
	IsMaster    bool
	// SocketProbed is false when the caller deliberately did not query the PVM
	// socket ("metrics --no-socket"). socket_reachable, quorate and is_master
	// are then omitted instead of being reported as 0, which would look like a
	// node that lost quorum to every alerting rule.
	SocketProbed bool
	Now          time.Time
}

// LastRun mirrors runner.LastRun (kept local so the metrics package has no
// dependency on the runner and stays trivial to test).
type LastRun struct {
	Hook       string    `json:"hook"`
	Phase      string    `json:"phase"`
	Event      string    `json:"event"`
	Exit       int       `json:"exit"`
	Attempts   int       `json:"attempts"`
	DurationMS int64     `json:"duration_ms"`
	TimedOut   bool      `json:"timed_out"`
	At         time.Time `json:"at"`
}

// PhaseInfo describes one phase directory.
type PhaseInfo struct {
	Enabled bool
	Hooks   int
	On      int
}

// LogEvent is the subset of a JSON log record the exporter cares about.
type LogEvent struct {
	TS       string `json:"ts"`
	Event    string `json:"event"`
	Phase    string `json:"phase"`
	Hook     string `json:"hook"`
	Exit     int    `json:"exit"`
	Repo     string `json:"-"`
	Duration int64  `json:"duration_ms"`
}

// ParseLogLines decodes JSON log records, skipping anything unparseable (a
// hook may have written a plain line into the file).
func ParseLogLines(lines []string) []LogEvent {
	out := make([]LogEvent, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || line[0] != '{' {
			continue
		}
		var ev LogEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out
}

// ReadLastRunsFile loads the runner's last-run registry, tolerating a missing
// or damaged file.
func ReadLastRunsFile(path string) map[string]LastRun {
	out := map[string]LastRun{}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return out
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return map[string]LastRun{}
	}
	return out
}
