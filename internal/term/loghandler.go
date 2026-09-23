package term

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Handler is a slog.Handler for a person reading a terminal.
//
// One line per record: the time, a marker coloured by level, the message, and the
// attributes after it, dimmed. What it leaves out is what a person does not need —
// the date, the year, the level spelled out — because the marker already says how
// bad it is and the date is the same date as the terminal it is being read in.
//
// It is not a general-purpose handler and does not try to be. Anything that parses
// logs wants slog's own text or JSON handler; this one is what an application gets
// when it configured no logger and its output is going to a terminal, which is
// exactly the case where a machine is not reading.
type Handler struct {
	mu     *sync.Mutex
	w      io.Writer
	style  Style
	level  slog.Leveler
	attrs  []slog.Attr
	groups []string
}

var _ slog.Handler = (*Handler)(nil)

// NewHandler returns a Handler writing to w at level.
func NewHandler(w io.Writer, level slog.Leveler) *Handler {
	if level == nil {
		level = slog.LevelInfo
	}
	return &Handler{mu: &sync.Mutex{}, w: w, style: NewStyle(w), level: level}
}

func (h *Handler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	next := *h
	next.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &next
}

func (h *Handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.groups = append(append([]string{}, h.groups...), name)
	return &next
}

func (h *Handler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder

	when := record.Time
	if when.IsZero() {
		when = time.Now()
	}
	line.WriteString(h.style.Dim(when.Format("15:04:05")))
	line.WriteByte(' ')
	line.WriteString(h.marker(record.Level))
	line.WriteByte(' ')
	line.WriteString(record.Message)

	// Attributes are dimmed and come last, because they are what a reader scans
	// for after the message has told them whether to care.
	var attrs strings.Builder
	for _, attr := range h.attrs {
		h.appendAttr(&attrs, attr)
	}
	record.Attrs(func(attr slog.Attr) bool {
		h.appendAttr(&attrs, attr)
		return true
	})
	if attrs.Len() > 0 {
		line.WriteString(h.style.Dim(attrs.String()))
	}
	line.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line.String())
	return err
}

// marker is the level, as one coloured character.
func (h *Handler) marker(level slog.Level) string {
	switch {
	case level >= slog.LevelError:
		return h.style.Fail(h.style.Mark("✗", "x"))
	case level >= slog.LevelWarn:
		return h.style.Warn(h.style.Mark("▲", "!"))
	case level >= slog.LevelInfo:
		return h.style.Info(h.style.Mark("•", "-"))
	default:
		return h.style.Dim(h.style.Mark("·", "."))
	}
}

// appendAttr writes one attribute, flattening a group into dotted keys.
func (h *Handler) appendAttr(out *strings.Builder, attr slog.Attr) {
	attr.Value = attr.Value.Resolve()
	if attr.Equal(slog.Attr{}) {
		return
	}
	if attr.Value.Kind() == slog.KindGroup {
		for _, nested := range attr.Value.Group() {
			h.appendAttr(out, slog.Attr{Key: attr.Key + "." + nested.Key, Value: nested.Value})
		}
		return
	}

	key := attr.Key
	if len(h.groups) > 0 {
		key = strings.Join(h.groups, ".") + "." + key
	}
	fmt.Fprintf(out, "  %s=%v", key, attr.Value.Any()) // any: slog.Value's own accessor
}
