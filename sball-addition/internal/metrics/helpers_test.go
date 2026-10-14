package metrics

import "os"

// writeFile is a tiny test helper.
func writeFile(t interface{ Helper() }, path, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}
