package collage_test

import (
	"net"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// A program the collage binary runs only to ask it something, whose main never
// hands the word to DispatchCommands, fails with the reason instead of serving
// a server the CLI would wait on forever.
func TestListenAndServe_RefusesWhenTheCLIAsked(t *testing.T) {
	t.Setenv("COLLAGE_CLI_COMMAND", "collage-inspect")
	addr := freeAddr(t)
	host, portText, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portText)
	app, err := collage.New(&collage.Config{
		Server:   collage.ServerConfig{Host: host, Port: port},
		Template: collage.TemplateConfig{FS: fstest.MapFS{"t/keep.html": {Data: []byte(`x`)}}, Root: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- app.ListenAndServe() }()
	select {
	case err := <-served:
		if err == nil || !strings.Contains(err.Error(), `"collage-inspect"`) || !strings.Contains(err.Error(), "DispatchCommands") {
			t.Fatalf("err = %v, want the word asked with and DispatchCommands named", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ListenAndServe served while the CLI was waiting for an answer")
	}
	// Nothing was listening: the address is still free.
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("the address was taken: %v", err)
	}
	l.Close()
}
