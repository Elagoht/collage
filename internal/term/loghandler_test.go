package term

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func logged(t *testing.T, write func(*slog.Logger)) string {
	t.Helper()
	var buf bytes.Buffer
	write(slog.New(NewHandler(&buf, slog.LevelDebug)))
	return buf.String()
}

// A buffer is not a terminal, so nothing escapes into it.
func TestHandler_PlainWhenNotATerminal(t *testing.T) {
	out := logged(t, func(l *slog.Logger) { l.Warn("something", "page", "home") })

	if strings.Contains(out, "\x1b[") {
		t.Errorf("output carries escape codes: %q", out)
	}
	if strings.ContainsAny(out, "✗▲•·") {
		t.Errorf("output carries terminal symbols: %q", out)
	}
}

func TestHandler_WritesTheMessageAndItsAttributes(t *testing.T) {
	out := logged(t, func(l *slog.Logger) {
		l.Warn("no key set", "action", "signup:POST", "count", 2)
	})

	for _, want := range []string{"no key set", "action=signup:POST", "count=2"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
	if lines := strings.Count(out, "\n"); lines != 1 {
		t.Errorf("output is %d lines, want one per record: %q", lines, out)
	}
}

// Each level has its own marker, so a reader scanning a terminal sees severity
// before reading a word of the message.
func TestHandler_LevelsAreDistinguishable(t *testing.T) {
	seen := map[string]string{}
	for name, write := range map[string]func(*slog.Logger){
		"debug": func(l *slog.Logger) { l.Debug("m") },
		"info":  func(l *slog.Logger) { l.Info("m") },
		"warn":  func(l *slog.Logger) { l.Warn("m") },
		"error": func(l *slog.Logger) { l.Error("m") },
	} {
		out := logged(t, write)
		marker := strings.Fields(out)[1]
		for other, existing := range seen {
			if existing == marker {
				t.Errorf("%s and %s share the marker %q", name, other, marker)
			}
		}
		seen[name] = marker
	}
}

func TestHandler_HonoursItsLevel(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewHandler(&buf, slog.LevelWarn)).Info("quiet")
	if buf.Len() != 0 {
		t.Errorf("output = %q for a record below the level, want nothing", buf.String())
	}
}

// WithAttrs and WithGroup are part of the contract, and a handler that drops them
// loses the context a caller deliberately attached.
func TestHandler_CarriesAttrsAndGroups(t *testing.T) {
	out := logged(t, func(l *slog.Logger) {
		l.With("page", "home").WithGroup("cache").Info("hit", "key", "abc")
	})

	for _, want := range []string{"page=home", "cache.key=abc"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q, want it to contain %q", out, want)
		}
	}
}

// A record with no time of its own still gets one: a log line without a time is a
// line nobody can place.
func TestHandler_SuppliesATimeWhenTheRecordHasNone(t *testing.T) {
	var buf bytes.Buffer
	h := NewHandler(&buf, slog.LevelInfo)
	if err := h.Handle(context.Background(), slog.NewRecord(time.Time{}, slog.LevelInfo, "m", 0)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if first := strings.Fields(buf.String())[0]; !strings.Contains(first, ":") {
		t.Errorf("first field = %q, want a time", first)
	}
}
