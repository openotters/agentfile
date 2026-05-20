package system

import "os"

// osStat is overridable in tests. The default is os.Stat; tests can
// swap it via package var if they need to simulate "BIN exists" vs
// "BIN missing" without touching the real filesystem.
//
//nolint:gochecknoglobals // intentional test seam
var osStat = os.Stat
