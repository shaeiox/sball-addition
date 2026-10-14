package hook

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"syscall"
)

// SetEnabled rewrites the ENABLED field inside the header of a hook file. The
// hook file remains the single source of truth for its state, which keeps
// enable/disable idempotent and survives config management.
//
// The original file mode is preserved and the write is atomic (temp file +
// rename) so a hook is never left half written.
func SetEnabled(path string, enabled bool) (changed bool, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	begin, end := -1, -1
	for i, raw := range lines {
		l := strings.TrimSpace(raw)
		if begin < 0 && strings.Contains(l, headerBegin) {
			begin = i
			continue
		}
		if begin >= 0 && strings.Contains(l, headerEnd) {
			end = i
			break
		}
	}
	if begin < 0 || end < 0 {
		return false, fmt.Errorf("%s: missing or unterminated SBALL-ADDITION header", path)
	}

	value := "false"
	if enabled {
		value = "true"
	}

	fieldLine := regexp.MustCompile(`^(\s*#\s*)ENABLED(\s*:\s*)([^\s#]*)(.*)$`)
	replaced := false
	for i := begin + 1; i < end; i++ {
		m := fieldLine.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		if m[3] == value {
			return false, nil // already in the desired state
		}
		// Keep column alignment and any trailing comment.
		pad := ""
		if len(value) < len(m[3]) {
			pad = strings.Repeat(" ", len(m[3])-len(value))
		}
		lines[i] = m[1] + "ENABLED" + m[2] + value + pad + m[4]
		replaced = true
		break
	}
	if !replaced {
		line := "# ENABLED: " + value
		out := make([]string, 0, len(lines)+1)
		out = append(out, lines[:end]...)
		out = append(out, line)
		out = append(out, lines[end:]...)
		lines = out
	}

	newData := []byte(strings.Join(lines, "\n"))
	tmp := path + ".sball-addition.tmp"
	if err := os.WriteFile(tmp, newData, info.Mode().Perm()); err != nil {
		return false, fmt.Errorf("write %s: %w", tmp, err)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		// Ownership is best effort: a non-root caller keeps its own uid.
		_ = os.Chown(tmp, int(st.Uid), int(st.Gid))
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("replace %s: %w", path, err)
	}
	return true, nil
}
