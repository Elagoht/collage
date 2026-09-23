package core

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/Elagoht/collage/internal/term"
)

// defaultLogger returns the logger an application that configured none gets.
//
// slog.Default(), unless two things are true: the output is going to a terminal,
// and nothing has replaced slog's own default handler. Then it is a handler meant
// for a person — one line, a coloured marker for the level, the time without the
// date, and the attributes dimmed after the message.
//
// The second condition is what keeps this from being rude. An application that
// called slog.SetDefault chose a handler, and choosing one is a decision the
// framework has no business overriding because it happened to notice a terminal.
// Detecting that is a type-name comparison against an unexported stdlib type, which
// is a hack — and a safe one, because the only thing a future rename can do is send
// this down the slog.Default() path, which is where it started.
func defaultLogger() *slog.Logger {
	if !term.NewStyle(os.Stderr).Rich() {
		return slog.Default()
	}
	if fmt.Sprintf("%T", slog.Default().Handler()) != "*slog.defaultHandler" {
		return slog.Default()
	}
	return slog.New(term.NewHandler(os.Stderr, slog.LevelInfo))
}
