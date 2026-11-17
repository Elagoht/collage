package cli

import (
	"os"
	"syscall"
)

// ShutdownSignals are the signals that mean "stop, and clean up": Ctrl-C, a
// terminate, and the hangup a closed terminal sends. A command that traps fewer
// is killed by the rest with no defer run — "collage dev" left the app it had
// started listening, reparented, and its build directory behind; inspect, routes
// and check left a collage-run-* directory holding a whole binary. main turns
// them into a cancelled context once, and every command stops through that.
var ShutdownSignals = []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP}
