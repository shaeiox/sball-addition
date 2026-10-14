package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"sball-addition/internal/lock"
)

// HookState is the RUN_ONCE marker written to
// /var/lib/sball-addition/state/<phase>/<name>.state.
type HookState struct {
	Hook     string    `json:"hook"`
	Phase    string    `json:"phase"`
	Host     string    `json:"host"`
	FirstRun time.Time `json:"first_run"`
	LastRun  time.Time `json:"last_run"`
	Runs     int       `json:"runs"`
	LastExit int       `json:"last_exit"`
}

// ReadState loads a RUN_ONCE marker. A missing file returns (nil, nil).
func ReadState(path string) (*HookState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var st HookState
	if err := json.Unmarshal(data, &st); err != nil {
		// A corrupt marker still counts as "already ran": the hook said it must
		// not run twice, and refusing to run is the safe interpretation.
		return &HookState{}, nil
	}
	return &st, nil
}

// WriteState writes a RUN_ONCE marker atomically.
func WriteState(path string, st *HookState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state directory %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// RemoveState deletes a RUN_ONCE marker (used by uninstall-hook and --force
// semantics).
func RemoveState(path string) error {
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// LastRun is the most recent outcome of one hook, kept in
// /var/lib/sball-addition/runtime/last-run.json so that "status" can report
// what happened without parsing the log file.
type LastRun struct {
	Hook       string    `json:"hook"`
	Phase      string    `json:"phase"`
	Host       string    `json:"host"`
	Event      string    `json:"event"` // finish | fail | timeout
	Exit       int       `json:"exit"`
	Attempts   int       `json:"attempts"`
	DurationMS int64     `json:"duration_ms"`
	TimedOut   bool      `json:"timed_out"`
	At         time.Time `json:"at"`
}

// LastRunKey is the map key used in last-run.json.
func LastRunKey(phase, hookName string) string { return phase + "/" + hookName }

// ReadLastRuns loads the last-run registry. A missing file yields an empty map.
func ReadLastRuns(path string) (map[string]LastRun, error) {
	out := map[string]LastRun{}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return out, err
	}
	if len(data) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(data, &out); err != nil {
		// Do not lose status output because of a damaged runtime file.
		return map[string]LastRun{}, nil
	}
	return out, nil
}

// UpdateLastRun records the outcome of a hook, serialising concurrent updates
// through a lock file so the watcher and CLI never clobber each other.
func UpdateLastRun(path string, rec LastRun) error {
	lockPath := path + ".lock"
	return lock.WithLock(lockPath, func() error {
		all, err := ReadLastRuns(path)
		if err != nil {
			all = map[string]LastRun{}
		}
		all[LastRunKey(rec.Phase, rec.Hook)] = rec
		data, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			return err
		}
		data = append(data, '\n')
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		tmp := fmt.Sprintf("%s.tmp.%d", path, os.Getpid())
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}
