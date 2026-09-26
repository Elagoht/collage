package httpx

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A mounted handler serving a stream pushes its write deadline forward, and one
// serving a WebSocket takes the connection over. Both go through the wrapper that
// records the status, so both need it to let them through.
func TestStatusCapturingWriter_LetsControllersThrough(t *testing.T) {
	var deadlineErr, hijackErr error
	var captured *statusCapturingWriter
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = &statusCapturingWriter{ResponseWriter: w}
		controller := http.NewResponseController(captured)
		deadlineErr = controller.SetWriteDeadline(time.Now().Add(time.Minute))
		conn, rw, err := controller.Hijack()
		hijackErr = err
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: close\r\n\r\n")
		_ = rw.Flush()
	}))
	server.Start()
	defer server.Close()

	res, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	_, _ = bufio.NewReader(res.Body).ReadString('\n')

	if deadlineErr != nil {
		t.Errorf("SetWriteDeadline through the wrapper: %v", deadlineErr)
	}
	if hijackErr != nil {
		t.Errorf("Hijack through the wrapper: %v", hijackErr)
	}
	if captured.Status() != http.StatusSwitchingProtocols {
		t.Errorf("Status = %d, want 101 for an upgraded connection", captured.Status())
	}
	if !strings.HasPrefix(res.Status, "101") {
		t.Errorf("client saw %q", res.Status)
	}
}
