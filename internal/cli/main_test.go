package cli

import (
	"os"
	"strings"
	"testing"
)

// testGoFlags is the GOFLAGS every go command the tests start runs with.
// The tests build projects in fresh temporary directories, often against a
// fresh module cache, and without -trimpath the go tool keys each compiled
// package on its directory: every run would store a new copy of every package
// in GOCACHE, which grew it by gigabytes a day.
const testGoFlags = "-trimpath"

func TestMain(m *testing.M) {
	os.Setenv("GOFLAGS", strings.TrimSpace(os.Getenv("GOFLAGS")+" "+testGoFlags))
	os.Exit(m.Run())
}
