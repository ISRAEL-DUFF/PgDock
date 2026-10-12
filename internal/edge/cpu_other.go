//go:build !unix

package edge

import "time"

// processCPU isn't measured off Unix.
func processCPU() (time.Duration, bool) { return 0, false }
