//go:build linux

package edge

import (
	"os"
	"strconv"
	"syscall"
)

// RenderWorkerASEnv caps a render worker's address space, in bytes
// (RLIMIT_AS): a decoder that allocates past it fails instead of taking
// the host's memory. GOMEMLIMIT sets the Go heap's soft limit beside it.
const RenderWorkerASEnv = "PGDOCK_RENDER_WORKER_AS_BYTES"

func limitMemory() {
	n, err := strconv.ParseUint(os.Getenv(RenderWorkerASEnv), 10, 64)
	if err != nil || n == 0 {
		return
	}
	_ = syscall.Setrlimit(syscall.RLIMIT_AS, &syscall.Rlimit{Cur: n, Max: n})
}
