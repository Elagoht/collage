package collage_test

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// docs/routing.md says a segment that fails to percent-decode is a 404. Through a
// real server that is not what the wire shows: net/http parses the request line
// first and answers 400 itself for an escape that is not one, before the router
// is asked. What does reach the router, and is a 404 there, is an escape that
// decodes fine into something no route can match — an encoded slash or a NUL.
func TestMalformedPercentEncodingOnTheWire(t *testing.T) {
	server := httptest.NewServer(slashApp(t, false).Handler())
	defer server.Close()

	status := func(target string) int {
		t.Helper()
		conn, err := net.Dial("tcp", server.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("GET " + target + " HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n")); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		if err != nil {
			t.Fatalf("GET %s: %v", target, err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	for target, want := range map[string]int{
		"/about":      http.StatusOK,
		"/blog/a%zz":  http.StatusBadRequest,
		"/blog/a%":    http.StatusBadRequest,
		"/blog/a%2fb": http.StatusNotFound,
		"/blog/a%00b": http.StatusNotFound,
	} {
		if got := status(target); got != want {
			t.Errorf("GET %s = %d, want %d", target, got, want)
		}
	}
}
