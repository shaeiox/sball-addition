package watcher

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sball-addition/internal/config"
	"sball-addition/internal/log"
	"sball-addition/internal/pvm"
	"sball-addition/internal/pvmtest"
)

// newEnv builds a temporary installation with one post-quorate hook and a fake
// PVM socket.
func newEnv(t *testing.T, hookBody string) (*config.Config, *pvmtest.Server, string) {
	t.Helper()
	root := t.TempDir()
	cfg := config.Defaults(root)
	for _, dir := range []string{cfg.Paths.PreDir, cfg.Paths.PostDir, cfg.Paths.StateDir, cfg.Paths.RuntimeDir, cfg.Paths.LogDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	srv, err := pvmtest.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)

	// The fake server must present this host as the local node.
	hostname := pvm.LocalName()
	srv.SetNodes(pvmtest.Node{ID: "1", Name: hostname, NodeState: "up", PVMState: "quorate", IP: "127.0.0.1", Port: "22"})
	cfg.General.SocketHost = srv.Host()
	cfg.General.SocketPort = srv.Port()

	marker := filepath.Join(cfg.Paths.RuntimeDir, "hook-ran")
	if hookBody == "" {
		hookBody = "touch " + marker + "\n"
	}
	hook := "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: post-quorate\n# NAME: demo\n" +
		"### END SBALL-ADDITION INFO\n" + hookBody
	path := filepath.Join(cfg.Paths.PostDir, "10-demo.sh")
	if err := os.WriteFile(path, []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg, srv, marker
}

func quietLogger() *log.Logger { return log.NewStderr(io.Discard, log.Debug) }

func TestWatcherRunsPostHooksAfterQuorate(t *testing.T) {
	cfg, srv, marker := newEnv(t, "")
	srv.QuorateAfter(700 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	start := time.Now()
	err := Run(ctx, Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 100 * time.Millisecond,
		PollTimeout:  15 * time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	elapsed := time.Since(start)

	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the post-quorate hook did not run: %v", err)
	}
	// The node became quorate after ~0.7s and the poll interval is 100ms, so a
	// generous bound still proves the "within seconds" criterion.
	if elapsed > 5*time.Second {
		t.Errorf("hooks took %s to run, want well under 5s", elapsed)
	}
	if _, err := os.Stat(cfg.Paths.PidFile()); !os.IsNotExist(err) {
		t.Errorf("the pid file must be removed on exit: %v", err)
	}
}

func TestWatcherPollTimeout(t *testing.T) {
	cfg, srv, marker := newEnv(t, "")
	srv.QuorateAfter(time.Hour) // never quorate

	err := Run(context.Background(), Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  400 * time.Millisecond,
	})
	if !errors.Is(err, ErrPollTimeout) {
		t.Fatalf("err = %v, want ErrPollTimeout", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hooks must not run after a poll timeout")
	}
}

func TestWatcherOnceKeepsQuietWhenNotQuorate(t *testing.T) {
	cfg, srv, marker := newEnv(t, "")
	srv.QuorateAfter(time.Hour)

	start := time.Now()
	if err := Run(context.Background(), Options{
		Config: cfg,
		Logger: quietLogger(),
		Once:   true,
	}); err != nil {
		t.Fatalf("--once must not fail when the node is not quorate yet: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Error("--once must return immediately")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hooks must not run while the node is not quorate")
	}
}

func TestWatcherLock(t *testing.T) {
	cfg, srv, _ := newEnv(t, "")
	srv.QuorateAfter(10 * time.Second)

	// A first watcher holds the lock; a second one must refuse to start.
	first := make(chan error, 1)
	go func() {
		first <- Run(context.Background(), Options{
			Config:       cfg,
			Logger:       quietLogger(),
			PollInterval: 50 * time.Millisecond,
			PollTimeout:  700 * time.Millisecond,
		})
	}()
	time.Sleep(300 * time.Millisecond)

	err := Run(context.Background(), Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  500 * time.Millisecond,
	})
	if !errors.Is(err, ErrLockHeld) {
		t.Fatalf("err = %v, want ErrLockHeld", err)
	}

	cancelErr := <-first
	if !errors.Is(cancelErr, ErrPollTimeout) {
		t.Fatalf("first watcher err = %v, want ErrPollTimeout", cancelErr)
	}
}

func TestWatcherSignalCancellation(t *testing.T) {
	cfg, srv, marker := newEnv(t, "")
	srv.QuorateAfter(time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	err := Run(ctx, Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("a cancelled watcher must exit cleanly: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hooks must not run after cancellation")
	}
}

func TestWatcherAbortsWhenPostHooksAbort(t *testing.T) {
	body := "# ON_FAIL: abort\n"
	cfg, srv, _ := newEnv(t, body)
	srv.QuorateAfter(10 * time.Millisecond)
	// overwrite the hook with a failing script
	hook := "#!/bin/bash\n### BEGIN SBALL-ADDITION INFO\n# HOOK: post-quorate\n# NAME: demo\n" +
		"# ON_FAIL: abort\n### END SBALL-ADDITION INFO\nexit 9\n"
	if err := os.WriteFile(filepath.Join(cfg.Paths.PostDir, "10-demo.sh"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Run(context.Background(), Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  10 * time.Second,
	})
	if err == nil {
		t.Fatal("a hook with ON_FAIL=abort must fail the watcher")
	}
}

func TestWatcherSkipsDisabledPhase(t *testing.T) {
	cfg, srv, marker := newEnv(t, "")
	srv.QuorateAfter(10 * time.Millisecond)
	cfg.PostQuorate.Enabled = false

	if err := Run(context.Background(), Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  5 * time.Second,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("hooks must not run when post-quorate is disabled")
	}
}

func TestWatcherRejectsBrokenHookSet(t *testing.T) {
	cfg, srv, _ := newEnv(t, "")
	srv.QuorateAfter(10 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(cfg.Paths.PostDir, "20-broken.sh"), []byte("#!/bin/bash\necho hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Run(context.Background(), Options{
		Config:       cfg,
		Logger:       quietLogger(),
		PollInterval: 50 * time.Millisecond,
		PollTimeout:  5 * time.Second,
	})
	if err == nil {
		t.Fatal("an invalid hook set must stop the watcher")
	}
}
