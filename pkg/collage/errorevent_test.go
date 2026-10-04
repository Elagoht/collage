package collage_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/Elagoht/collage/pkg/collage"
)

// eventRecorder is an ErrorHook plugin that keeps every event it is handed.
type eventRecorder struct {
	mu     sync.Mutex
	events []collage.ErrorEvent
}

func (*eventRecorder) Name() string                             { return "test/events" }
func (*eventRecorder) Version() string                          { return "0" }
func (*eventRecorder) Init(context.Context, collage.Host) error { return nil }
func (*eventRecorder) Shutdown(context.Context) error           { return nil }
func (e *eventRecorder) OnError(_ context.Context, ev *collage.ErrorEvent) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.events = append(e.events, *ev)
	return nil
}

// at returns the events recorded for path.
func (e *eventRecorder) at(path string) []collage.ErrorEvent {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []collage.ErrorEvent
	for _, ev := range e.events {
		if ev.Path == path {
			out = append(out, ev)
		}
	}
	return out
}

// lockedBuffer is a log sink safe to write from the handler and read from the test.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func errorEventApp(t *testing.T, rec *eventRecorder, logs *lockedBuffer) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<p>{{.Data}}</p>`)},
		}, Root: "t"},
		Plugins: []collage.Plugin{rec},
		Logger:  slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return app
}

func failingData() collage.DataHandlerFunc {
	return collage.Load(func(context.Context, *collage.RenderContext) (string, error) {
		return "", errors.New("boom")
	})
}

// Every failure answered to a reader tells ErrorHook which status it was answered
// with, and carries the request it came from.
func TestErrorEvent_CarriesStatusAndRequest(t *testing.T) {
	rec := &eventRecorder{}
	logs := &lockedBuffer{}
	app := errorEventApp(t, rec, logs)

	page := collage.NewPage("broken").
		WithContent(collage.NewFragment("body", "p.html").WithDataHandler(failingData()).Build()).
		WithPath("en", "/broken").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}
	live := collage.NewFragment("live", "p.html").WithDataHandler(failingData()).Required().Build()
	host := collage.NewPage("host").
		WithContent(collage.NewFragment("ok", "p.html").Build()).
		WithPath("en", "/host").WithFragmentPath("en", "/host/live", live).Build()
	if err := app.RegisterPage(host); err != nil {
		t.Fatal(err)
	}
	action := collage.NewAction("send").WithPath("en", "/send").WithMethods(http.MethodPost).WithoutCSRF().
		WithHandler(func(context.Context, *collage.RenderContext) (*collage.ActionResult, error) {
			return nil, errors.New("action failed")
		}).Build()
	if err := app.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	doc := collage.NewDocument("feed", "application/xml").WithPath("en", "/feed.xml").
		WithHandler(func(context.Context, *collage.RenderContext) ([]byte, []string, error) {
			return nil, nil, errors.New("document failed")
		}).Build()
	if err := app.RegisterDocument(doc); err != nil {
		t.Fatal(err)
	}
	if err := app.Mount("/files/", fstest.MapFS{}); err != nil {
		t.Fatal(err)
	}
	inner := errors.New("inner")
	if err := app.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(inner) })); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		method, path string
		status       int
	}{
		{http.MethodGet, "/broken", http.StatusInternalServerError},
		{http.MethodGet, "/host/live", 0},
		{http.MethodPost, "/send", 0},
		{http.MethodGet, "/feed.xml", 0},
		{http.MethodGet, "/files/missing.txt", http.StatusNotFound},
		{http.MethodGet, "/boom", http.StatusInternalServerError},
	}
	handler := app.Handler()
	for _, c := range cases {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if c.status != 0 && w.Code != c.status {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, w.Code, c.status)
		}
		if w.Code < 400 {
			t.Errorf("%s %s = %d, want a failure", c.method, c.path, w.Code)
		}
		events := rec.at(c.path)
		if len(events) == 0 {
			t.Errorf("%s %s: no ErrorEvent", c.method, c.path)
			continue
		}
		for _, ev := range events {
			if ev.Status != w.Code {
				t.Errorf("%s %s: ev.Status = %d, want %d (stage %q)", c.method, c.path, ev.Status, w.Code, ev.Stage)
			}
			if ev.Request == nil || ev.Request.URL.Path != c.path {
				t.Errorf("%s %s: ev.Request = %v, want the request for %s", c.method, c.path, ev.Request, c.path)
			}
		}
	}

	events := rec.at("/boom")
	if len(events) != 1 {
		t.Fatalf("/boom: %d events, want 1", len(events))
	}
	ev := events[0]
	if !errors.Is(ev.Err, collage.ErrPanic) {
		t.Errorf("panic: errors.Is(ErrPanic) = false for %v", ev.Err)
	}
	var pe *collage.PanicError
	if !errors.As(ev.Err, &pe) {
		t.Fatalf("panic: errors.As(*PanicError) = false for %v", ev.Err)
	}
	if len(pe.Stack) == 0 {
		t.Error("panic: PanicError.Stack is empty")
	}
	if !errors.Is(ev.Err, inner) {
		t.Errorf("panic: the panic value is not reachable through %v", ev.Err)
	}

	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "path=/boom") && strings.Contains(l, "stage=panic") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no log line for the panic in:\n%s", logs.String())
	}
	if !strings.Contains(line, "stack=") {
		t.Errorf("panic log line has no stack attribute: %s", line)
	}
	_, after, _ := strings.Cut(line, "error=")
	errValue, _, _ := strings.Cut(after, " stack=")
	if strings.Contains(errValue, "goroutine ") {
		t.Errorf("panic log line's error carries the stack: %s", errValue)
	}
}

// An error page that cannot render is reported under its own stage, with the
// status the failure is answered with.
func TestErrorEvent_ErrorPageFailureIsA500(t *testing.T) {
	rec := &eventRecorder{}
	app := errorEventApp(t, rec, &lockedBuffer{})

	errorPage := collage.NewPage("oops").
		WithContent(collage.NewFragment("oops", "p.html").WithDataHandler(failingData()).Build()).Build()
	if err := app.RegisterErrorPage(errorPage); err != nil {
		t.Fatal(err)
	}
	page := collage.NewPage("broken").
		WithContent(collage.NewFragment("body", "p.html").WithDataHandler(failingData()).Build()).
		WithPath("en", "/broken").Build()
	if err := app.RegisterPage(page); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/broken", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("GET /broken = %d, want 500", w.Code)
	}
	found := false
	for _, ev := range rec.at("/broken") {
		if ev.Stage != "error_page" {
			continue
		}
		found = true
		if ev.Status != http.StatusInternalServerError {
			t.Errorf("error_page: ev.Status = %d, want 500", ev.Status)
		}
		if ev.Request == nil || ev.Request.URL.Path != "/broken" {
			t.Errorf("error_page: ev.Request = %v, want the request for /broken", ev.Request)
		}
	}
	if !found {
		t.Errorf("no error_page event among %+v", rec.at("/broken"))
	}
}

// A panic in an app.Handle handler is answered in plain text; in development
// that answer shows the panic's stack, as the HTML dev page does, and in
// production it shows neither the panic value nor the stack.
func TestPanicPlainTextAnswer(t *testing.T) {
	for _, dev := range []bool{true, false} {
		app, err := collage.New(&collage.Config{
			Server:   collage.ServerConfig{Host: "localhost", Port: 3000},
			DevMode:  dev,
			Template: collage.TemplateConfig{FS: fstest.MapFS{"t/p.html": {Data: []byte(`<p>hi</p>`)}}, Root: "t"},
			Logger:   slog.New(slog.NewTextHandler(&lockedBuffer{}, nil)),
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Handle("/boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("kaboom-value") })); err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		app.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
		body := w.Body.String()
		if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatalf("dev %v: %d %q, want a plain-text 500", dev, w.Code, w.Header().Get("Content-Type"))
		}
		hasStack, hasValue := strings.Contains(body, "goroutine "), strings.Contains(body, "kaboom-value")
		if dev && (!hasStack || !hasValue) {
			t.Errorf("development body lacks the panic or its stack:\n%s", body)
		}
		if !dev && (hasStack || hasValue) {
			t.Errorf("production body shows the panic:\n%s", body)
		}
	}
}
