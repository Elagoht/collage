package cli

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// devReloadPath is the event stream a development page listens on for a reason
// to reload itself. It is the program's own — see internal/httpx — and is
// answered here only while there is no program to answer it.
const devReloadPath = "/_collage/reload"

// devOutputLimit is how much of a build's or a program's output is kept for the
// error page. The end is what is kept: that is where the reason it stopped is.
const devOutputLimit = 64 << 10

// devListenTimeout is how long a request waits for a started program to
// listen before the page says it has not. A variable so the tests can shorten
// it.
var devListenTimeout = 10 * time.Second

// devState is where the program behind a devProxy is.
type devState int

const (
	// devStarting: being built, or started and not listening yet. A request
	// waits for it to become one of the other two.
	devStarting devState = iota
	// devReady: listening. A request is passed on.
	devReady
	// devDown: exited, or never built. A request is answered with why.
	devDown
)

// devProxy is what the browser talks to during "collage dev". It passes each
// request on to the program while the program is listening, holds it while the
// program is starting, and answers it with the program's output when there is
// no program — so a crash is seen where the page was expected, not only in the
// terminal.
type devProxy struct {
	// public is the address the browser uses; target is the one the program is
	// told to listen on.
	public string
	target string
	proxy  *httputil.ReverseProxy

	mu    sync.Mutex
	state devState
	// owner is the process a devStarting state is waiting on, so a process
	// that has already been replaced cannot mark the proxy ready.
	owner  *devProcess
	output string
	// generation counts state changes. It names the "program" a down page's
	// reload stream says it is served by, so every change reloads it.
	generation int
	// changed is closed, and replaced, on every state change.
	changed chan struct{}
}

func newDevProxy(public, target string) *devProxy {
	p := &devProxy{public: public, target: target, changed: make(chan struct{})}
	targetURL := &url.URL{Scheme: "http", Host: target}
	p.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
			// The Host the browser sent, not the program's own address: the
			// program builds links and checks origins against it.
			r.Out.Host = r.In.Host
		},
		ErrorHandler: p.proxyFailed,
	}
	return p
}

// set moves the proxy to state and wakes every request waiting on a change.
func (p *devProxy) set(state devState, owner *devProcess, output string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.state, p.owner, p.output = state, owner, output
	p.generation++
	close(p.changed)
	p.changed = make(chan struct{})
}

// starting says a build is under way, or that process has been started.
func (p *devProxy) starting(process *devProcess) { p.set(devStarting, process, "") }

// down says there is no program, and output is why.
func (p *devProxy) down(output string) { p.set(devDown, nil, output) }

// awaitListening marks the proxy ready once process accepts a connection, or
// gives up when ctx — the process's own — is done.
func (p *devProxy) awaitListening(ctx context.Context, process *devProcess) {
	var dialer net.Dialer
	for {
		dialCtx, cancel := context.WithTimeout(ctx, time.Second)
		conn, err := dialer.DialContext(dialCtx, "tcp", p.target)
		cancel()
		if err == nil {
			conn.Close()
			p.mu.Lock()
			current := p.state == devStarting && p.owner == process
			p.mu.Unlock()
			if current {
				p.set(devReady, nil, "")
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(25 * time.Millisecond):
		}
	}
}

func (p *devProxy) snapshot() (devState, string, int, chan struct{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state, p.output, p.generation, p.changed
}

// started reports whether the program has been started, rather than being
// built, so that what is left is for it to listen.
func (p *devProxy) started() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state == devStarting && p.owner != nil
}

func (p *devProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A build takes as long as it takes, so only a program already started is
	// timed: one that never listens where it was told would otherwise hold the
	// page forever. The reload stream is not timed at all — waiting is its job.
	var timeout <-chan time.Time
	for {
		state, output, generation, changed := p.snapshot()
		switch state {
		case devReady:
			p.proxy.ServeHTTP(w, r)
			return
		case devDown:
			if r.URL.Path == devReloadPath {
				p.serveDownStream(w, r, generation, changed)
			} else {
				p.serveDown(w, output)
			}
			return
		}
		if timeout == nil && r.URL.Path != devReloadPath && p.started() {
			timer := time.NewTimer(devListenTimeout)
			defer timer.Stop()
			timeout = timer.C
		}
		select {
		case <-changed:
		case <-timeout:
			p.serveDown(w, fmt.Sprintf("collage: dev: the program is running, but nothing is listening on %s, the HOST and PORT it was started with.\nDoes main.go listen on the HOST and PORT in its environment?\n", p.target))
			return
		case <-r.Context().Done():
			return
		}
	}
}

// proxyFailed answers a request the program did not. That is almost always a
// program that has just exited, so it waits a moment for the exit to be heard
// and shows it, rather than a bare connection error.
func (p *devProxy) proxyFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	state, output, _, changed := p.snapshot()
	if state == devReady {
		select {
		case <-changed:
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
			return
		}
		state, output, _, _ = p.snapshot()
	}
	if state == devDown {
		p.serveDown(w, output)
		return
	}
	http.Error(w, "collage: dev: the program did not answer: "+err.Error(), http.StatusBadGateway)
}

// serveDownStream answers a page's reload stream while there is no program. It
// names itself after the current state, which differs from the program the
// page came from, so an open page reloads onto the error; and it ends when the
// state changes, so the page reconnects to whatever comes next.
func (p *devProxy) serveDownStream(w http.ResponseWriter, r *http.Request, generation int, changed chan struct{}) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, "retry: 500\nevent: hello\ndata: collage-dev-%d\n\n", generation)
	if http.NewResponseController(w).Flush() != nil {
		return
	}
	select {
	case <-changed:
	case <-r.Context().Done():
	}
}

// serveDown answers with output, the reason there is no program.
func (p *devProxy) serveDown(w http.ResponseWriter, output string) {
	header := w.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, devDownPage, html.EscapeString(output), html.EscapeString(p.public), html.EscapeString(p.target))
}

// devDownPage is the page served while there is no program: its output, and the
// reload stream every development page listens on.
const devDownPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>collage dev: the program is not running</title>
<style>
body{margin:0;padding:2rem;font:14px/1.5 ui-sans-serif,system-ui,sans-serif;background:#1b1b1f;color:#e8e8ea}
h1{margin:0 0 1rem;font-size:1.1rem;color:#ff8a80}
pre{margin:0;padding:1rem;overflow:auto;white-space:pre-wrap;word-break:break-word;background:#111114;border-radius:6px;font:13px/1.5 ui-monospace,SFMono-Regular,Menlo,monospace}
p{color:#a0a0a8}
</style>
</head>
<body>
<h1>The program is not running</h1>
<pre>%s</pre>
<p>collage dev serves %s and runs the program on %s. This page reloads itself when a change brings the program back.</p>
<script>(()=>{let id;const listen=()=>{const s=new EventSource("` + devReloadPath + `");` +
	`s.addEventListener("hello",e=>{if(id&&id!==e.data)location.reload();id=e.data});` +
	`s.onerror=()=>{if(s.readyState===EventSource.CLOSED)setTimeout(listen,500)}};listen()})()</script>
</body>
</html>
`

// devAddress is the address "collage dev" listens on: HOST and PORT as the
// program would read them — from the shell, else from env, the environment
// file — falling back to the scaffolded main.go's own defaults.
func devAddress(env []string, lookup func(string) (string, bool)) string {
	value := func(key, fallback string) string {
		if v, ok := lookup(key); ok && v != "" {
			return v
		}
		if v := envValueOf(env, key); v != "" {
			return v
		}
		return fallback
	}
	port := value("PORT", "3000")
	if _, err := strconv.Atoi(port); err != nil {
		port = "3000"
	}
	return net.JoinHostPort(value("HOST", "localhost"), port)
}

// envValueOf is the value the last KEY=value in env gives key.
func envValueOf(env []string, key string) string {
	var value string
	for _, pair := range env {
		if k, v, ok := strings.Cut(pair, "="); ok && k == key {
			value = v
		}
	}
	return value
}

// freeLoopbackAddress is a loopback address nothing is listening on right now,
// for the program to listen on.
func freeLoopbackAddress() (string, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer listener.Close()
	return listener.Addr().String(), nil
}

// tailBuffer keeps the last devOutputLimit bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, b...)
	if extra := len(t.buf) - devOutputLimit; extra > 0 {
		t.buf = append(t.buf[:0], t.buf[extra:]...)
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.buf)
}
