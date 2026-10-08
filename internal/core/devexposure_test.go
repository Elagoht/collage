package core

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
)

// A development server bound where other machines can reach it serves them its
// error pages and tooling: said once, at Warn, naming the address.
func TestWarnDevExposure(t *testing.T) {
	for _, tc := range []struct {
		addr string
		dev  bool
		warn bool
	}{
		{"0.0.0.0:6060", true, true},
		{"[::]:6060", true, true},
		{"192.168.1.20:6060", true, true},
		{"127.0.0.1:6060", true, false},
		{"[::1]:6060", true, false},
		{"0.0.0.0:6060", false, false},
	} {
		addr, err := net.ResolveTCPAddr("tcp", tc.addr)
		if err != nil {
			t.Fatal(err)
		}
		logger, logs := newCapturingLogger()
		warnDevExposure(logger, tc.dev, addr)
		got := logs.String()
		if warned := strings.Contains(got, "level=WARN"); warned != tc.warn {
			t.Errorf("dev=%v %s: warned=%v, want %v\n%s", tc.dev, tc.addr, warned, tc.warn, got)
		}
		if tc.warn && (strings.Count(got, "level=WARN") != 1 || !strings.Contains(got, "development") || !strings.Contains(got, addr.String())) {
			t.Errorf("%s: warning %q, want one naming development mode and the address", tc.addr, got)
		}
	}
}

// ListenAndServe makes the check with the address it bound: loopback, as every
// test here binds, is not warned about.
func TestListenAndServe_DevModeOnLoopbackIsNotWarned(t *testing.T) {
	var logs syncBuilder
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn}))
	app, _, served := startServing(t, func(c *Config) {
		c.DevMode = true
		c.Logger = logger
	})
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	waitServed(t, served)
	if strings.Contains(logs.String(), "development mode") {
		t.Errorf("a loopback development server was warned about:\n%s", logs.String())
	}
}

// syncBuilder is a strings.Builder a server goroutine may log into while the
// test reads it.
type syncBuilder struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuilder) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuilder) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
