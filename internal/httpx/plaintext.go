package httpx

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Elagoht/collage/internal/render"
)

// writePlainText writes a plain-text error response for a document. The content
// type of an error follows the route kind, not the request: an HTML error page
// returned to a crawler fetching sitemap.xml, or to a client expecting JSON, is the
// same mistake a page's HTML error page would be for either of them. In production
// the body is a single generic line; in dev mode it names the route and carries the
// error chain and, for a panic, its stack, as the HTML dev page does. The response
// always carries Cache-Control: no-store, since an error is never a representation
// worth caching.
func writePlainText(w http.ResponseWriter, r *http.Request, status int, devMode bool, route string, cause error) {
	body := http.StatusText(status) + "\n"
	if devMode && cause != nil {
		detail := fmt.Sprintf("%+v", cause)
		var panicErr *render.PanicError
		if errors.As(cause, &panicErr) && len(panicErr.Stack) > 0 {
			detail += "\n\n" + string(panicErr.Stack)
		}
		body = fmt.Sprintf("%s\n\nroute: %s\nerror: %s\n", http.StatusText(status), route, detail)
	}

	header := w.Header()
	header.Set("Content-Type", "text/plain; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)

	if r.Method != http.MethodHead {
		// The error is deliberately unchecked, matching writeBody: the status line
		// and headers are already on the wire, so there is nothing left to tell
		// the client, and a client that hung up mid-body is not a server fault
		// worth logging.
		_, _ = w.Write([]byte(body))
	}
}
