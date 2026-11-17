//go:build unix

package cli

import (
	"syscall"
	"testing"
	"time"
)

// A closed terminal sends SIGHUP. With only SIGINT and SIGTERM trapped it killed
// "collage dev" outright, with no defer run: the app it had started went on
// listening, reparented, and the collage-dev-* build directory stayed behind.
func TestDev_SIGHUPStopsItLikeCtrlC(t *testing.T) {
	runner, _, stop := devSession(t, map[string]string{"main.go": "package main"})
	waitStarted(t, runner)

	// Handled by dev's own NotifyContext; without that handler this would end
	// the test binary.
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- stop() }()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collage dev kept running after SIGHUP")
	}
}
