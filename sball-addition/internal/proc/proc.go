// Package proc provides small /proc based helpers (no external tools such as
// pidof/ps are required, which keeps sball-addition dependency free).
package proc

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Find returns the pid of the first process whose command name matches name.
// It returns 0 when no such process exists.
func Find(name string) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if Comm(pid) == name {
			return pid
		}
	}
	return 0
}

// Comm returns /proc/<pid>/comm (the process name, truncated to 15 chars by the
// kernel).
func Comm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// Alive reports whether a process with the given pid exists.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return Exists(fmt.Sprintf("/proc/%d", pid))
}

// Exists reports whether a path exists.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Cmdline returns the full command line of a process.
func Cmdline(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return ""
	}
	return strings.ReplaceAll(strings.TrimRight(string(data), "\x00"), "\x00", " ")
}

// Uptime returns how long a process has been running. It is derived from
// /proc/uptime and the start time recorded in /proc/<pid>/stat.
func Uptime(pid int) (time.Duration, bool) {
	if pid <= 0 {
		return 0, false
	}
	total, err := systemUptime()
	if err != nil {
		return 0, false
	}
	start, err := processStartSeconds(pid)
	if err != nil {
		return 0, false
	}
	d := total - start
	if d < 0 {
		d = 0
	}
	return time.Duration(d * float64(time.Second)), true
}

func systemUptime() (float64, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return 0, fmt.Errorf("unexpected /proc/uptime content")
	}
	return strconv.ParseFloat(fields[0], 64)
}

// processStartSeconds converts the starttime field of /proc/<pid>/stat into
// seconds since boot, assuming the usual 100 Hz clock tick.
func processStartSeconds(pid int) (float64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The comm field may contain spaces and parentheses; everything after the
	// last ')' is space separated, and starttime is field 22 overall (field 20
	// after the cut).
	s := string(data)
	idx := strings.LastIndex(s, ")")
	if idx < 0 || idx+2 >= len(s) {
		return 0, fmt.Errorf("unexpected /proc/%d/stat content", pid)
	}
	fields := strings.Fields(s[idx+2:])
	const startTimeOffset = 19 // zero based index of field 22 in the tail
	if len(fields) <= startTimeOffset {
		return 0, fmt.Errorf("unexpected /proc/%d/stat field count", pid)
	}
	ticks, err := strconv.ParseFloat(fields[startTimeOffset], 64)
	if err != nil {
		return 0, err
	}
	return ticks / 100.0, nil
}

// FormatDuration renders a duration the way the status command shows it:
// "12s", "3m", "2h", "4d".
func FormatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// FormatUptime renders a process uptime as hh:mm:ss like the PVM tools do.
func FormatUptime(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}
