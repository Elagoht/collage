package cli

import (
	"sync"
	"testing"
)

// A process that was replaced while it was starting must not be able to mark the
// proxy ready, and the answer is decided under the same lock that makes the
// change: hammering becomeReady against a rebuild must never leave the proxy
// ready for a process that is no longer its owner (run with -race).
func TestDevProxy_OnlyTheCurrentProcessMakesItReady(t *testing.T) {
	p := newDevProxy("127.0.0.1:0", "127.0.0.1:1")
	old, current := &devProcess{}, &devProcess{}

	p.starting(old)
	p.starting(current)
	p.becomeReady(old)
	if state, _, _, _ := p.snapshot(); state != devStarting {
		t.Fatalf("state = %v after a replaced process listened, want still starting", state)
	}
	p.becomeReady(current)
	if state, _, _, _ := p.snapshot(); state != devReady {
		t.Fatalf("state = %v after the current process listened, want ready", state)
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() { defer wg.Done(); p.becomeReady(old) }()
		go func() { defer wg.Done(); p.down("rebuilding") }()
	}
	wg.Wait()
	if state, _, _, _ := p.snapshot(); state == devReady {
		t.Fatal("proxy is ready with no process to be ready for")
	}
}
