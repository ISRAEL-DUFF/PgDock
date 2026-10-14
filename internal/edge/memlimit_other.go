//go:build !linux

package edge

// RenderWorkerASEnv is only applied on Linux.
const RenderWorkerASEnv = "XENIBASE_RENDER_WORKER_AS_BYTES"

func limitMemory() {}
