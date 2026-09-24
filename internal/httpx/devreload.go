package httpx

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"io/fs"
	"net/http"
	"sync"
	"time"
)

// devReloadPath is the event stream a development page listens on for a reason to
// reload itself. It exists only in development.
const devReloadPath = "/_collage/reload"

// devReloadInterval is how often the watched sources are looked at while a page is
// listening. A variable so the tests can shorten it.
var devReloadInterval = 300 * time.Millisecond

// devReloadScript is what a development page carries. EventSource reconnects by
// itself, which is what makes a restart visible: the server that answers the
// reconnect names itself differently, and a page that sees a new name was served
// by a program that no longer exists.
//
// It reconnects by hand once the browser gives up. A connection refused outright
// — the moment between one build stopping and the next listening — is one the
// standard lets a browser treat as final, and a page that stopped listening then
// would never reload again.
const devReloadScript = `<script>(()=>{let id;const listen=()=>{const s=new EventSource("` + devReloadPath + `");` +
	`s.addEventListener("hello",e=>{if(id&&id!==e.data)location.reload();id=e.data});` +
	`s.addEventListener("reload",()=>location.reload());` +
	`s.onerror=()=>{if(s.readyState===EventSource.CLOSED)setTimeout(listen,500)}};listen()})()</script>`

// reloadHub tells listening development pages to reload: when a template or a
// static file changes, and — by naming each process differently — when the program
// restarts.
//
// It watches only while somebody is listening. A development server with no
// browser open does no polling at all, and neither does a test.
type reloadHub struct {
	sources  []fs.FS
	instance string

	mu        sync.Mutex
	listeners map[chan struct{}]struct{}
	stopWatch context.CancelFunc
	closed    chan struct{}
	closeOnce sync.Once
}

func newReloadHub(sources []fs.FS) *reloadHub {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return &reloadHub{
		sources:   sources,
		instance:  hex.EncodeToString(id),
		listeners: make(map[chan struct{}]struct{}),
		closed:    make(chan struct{}),
	}
}

// ServeHTTP streams reload events to one page until it goes away or the server
// shuts down.
func (hub *reloadHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")

	// The server's WriteTimeout is for responses that end. This one does not, and
	// cutting it every thirty seconds would only make the page reconnect.
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Time{})

	events, unsubscribe := hub.subscribe()
	defer unsubscribe()

	// retry keeps the gap short while a rebuild is under way.
	fmt.Fprintf(w, "retry: 500\nevent: hello\ndata: %s\n\n", hub.instance)
	if controller.Flush() != nil {
		return
	}

	// A comment now and then, so a connection the browser dropped is noticed
	// here rather than held open forever.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-hub.closed:
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		case <-events:
			fmt.Fprint(w, "event: reload\ndata:\n\n")
		}
		if controller.Flush() != nil {
			return
		}
	}
}

// Close ends every stream, so a graceful shutdown is not held open by pages that
// would otherwise listen forever.
func (hub *reloadHub) Close() {
	hub.closeOnce.Do(func() { close(hub.closed) })
}

// subscribe adds a listener, starting the watch for the first one, and returns
// its events and the function that removes it — stopping the watch with the last.
func (hub *reloadHub) subscribe() (chan struct{}, func()) {
	events := make(chan struct{}, 1)

	hub.mu.Lock()
	hub.listeners[events] = struct{}{}
	if hub.stopWatch == nil {
		ctx, cancel := context.WithCancel(context.Background())
		hub.stopWatch = cancel
		go hub.watch(ctx)
	}
	hub.mu.Unlock()

	return events, func() {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		delete(hub.listeners, events)
		if len(hub.listeners) == 0 && hub.stopWatch != nil {
			hub.stopWatch()
			hub.stopWatch = nil
		}
	}
}

// watch looks at the sources every devReloadInterval and tells every listener
// when they change.
func (hub *reloadHub) watch(ctx context.Context) {
	last := hub.fingerprint()
	ticker := time.NewTicker(devReloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		next := hub.fingerprint()
		if next == last {
			continue
		}
		last = next

		hub.mu.Lock()
		for events := range hub.listeners {
			// Buffered by one and never blocking: a page already told to
			// reload does not need telling twice.
			select {
			case events <- struct{}{}:
			default:
			}
		}
		hub.mu.Unlock()
	}
}

// fingerprint summarises every file in the sources by name, size and time.
func (hub *reloadHub) fingerprint() uint64 {
	sum := fnv.New64a()
	for i, source := range hub.sources {
		_ = fs.WalkDir(source, ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			fmt.Fprintf(sum, "%d:%s:%d:%d\n", i, path, info.Size(), info.ModTime().UnixNano())
			return nil
		})
	}
	return sum.Sum64()
}

// withReloadScript returns html carrying the development reload script, before
// its closing body tag when it has one and at the end when it does not.
func withReloadScript(html []byte) []byte {
	i := bytes.LastIndex(bytes.ToLower(html), []byte("</body>"))
	if i < 0 {
		i = len(html)
	}
	out := make([]byte, 0, len(html)+len(devReloadScript))
	out = append(out, html[:i]...)
	out = append(out, devReloadScript...)
	return append(out, html[i:]...)
}
