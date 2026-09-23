package core

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// An application that chose a handler chose it. Noticing a terminal is not a reason
// to override a decision somebody made on purpose.
func TestDefaultLogger_DoesNotOverrideAConfiguredDefault(t *testing.T) {
	var buf bytes.Buffer
	chosen := slog.New(slog.NewJSONHandler(&buf, nil))

	previous := slog.Default()
	slog.SetDefault(chosen)
	t.Cleanup(func() { slog.SetDefault(previous) })

	got := defaultLogger()
	got.Info("hello")

	if !strings.Contains(buf.String(), `"msg":"hello"`) {
		t.Errorf("the application's own handler was not used; output = %q", buf.String())
	}
}

// And an application that configured none gets whatever the framework picks, which
// off a terminal is slog's own default — unchanged, so nothing that parses this
// output has to learn a new format.
func TestDefaultLogger_FallsBackToSlogDefault(t *testing.T) {
	// The test binary's stderr is not a terminal, so this is the path taken.
	if defaultLogger().Handler() != slog.Default().Handler() {
		t.Error("defaultLogger() returned something other than slog's default off a terminal")
	}
}
