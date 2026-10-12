package plugin

import (
	"context"
	"errors"
	"testing"
	"time"
)

// blockingPlugin is a testPlugin whose Init tells entered it has begun and then
// waits for release: a plugin still initialising when Shutdown runs.
type blockingPlugin struct {
	testPlugin
	entered chan struct{}
	release chan struct{}
}

func (p *blockingPlugin) Init(ctx context.Context, host Host) error {
	p.log.add("init:" + p.name)
	close(p.entered)
	<-p.release
	return nil
}

// A Shutdown while a plugin's Init is still running must not leave that plugin
// running once its Init returns, nor let Init go on to the plugins after it.
func TestRegistry_ShutdownDuringInit(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	slow := &blockingPlugin{
		testPlugin: testPlugin{name: "b", log: log},
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
	}
	mustRegister(t, r, &testPlugin{name: "a", log: log})
	mustRegister(t, r, slow)
	mustRegister(t, r, &testPlugin{name: "c", log: log})

	done := make(chan error, 1)
	go func() { done <- r.Init(context.Background(), hostFor) }()

	select {
	case <-slow.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("b's Init never began")
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	close(slow.release)

	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Init never returned")
	}
	if !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Init() error = %v, want ErrShuttingDown", err)
	}
	want := []string{"init:a", "init:b", "shutdown:a", "shutdown:b"}
	if got := log.get(); !equalStrings(got, want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
	// Nothing is left for a later Shutdown.
	if err := r.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown() error = %v, want nil", err)
	}
	if got := log.get(); !equalStrings(got, want) {
		t.Errorf("calls after a second Shutdown = %v, want %v", got, want)
	}
}

// A Shutdown before Init starts nothing.
func TestRegistry_ShutdownBeforeInit(t *testing.T) {
	log := &callLog{}
	r := NewRegistry(nil)
	mustRegister(t, r, &testPlugin{name: "a", log: log})
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v, want nil", err)
	}
	if err := r.Init(context.Background(), hostFor); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Init() error = %v, want ErrShuttingDown", err)
	}
	if got := log.get(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}
