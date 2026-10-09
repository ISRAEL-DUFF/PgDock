package edge

import (
	"runtime"
	"sync"
	"time"
)

// cpuSampler measures the edge process's share of its host's CPUs between
// reports (V4.1 §11), so pgdock-server can see a region's edges are busy
// and propose an edge node.
type cpuSampler struct {
	mu       sync.Mutex
	lastCPU  time.Duration
	lastWall time.Time
}

// sample is the percentage of all the host's CPUs the process used since
// the previous sample (nil on the first, or where the OS can't tell).
func (c *cpuSampler) sample() *float64 {
	used, ok := processCPU()
	if !ok {
		return nil
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	prevCPU, prevWall := c.lastCPU, c.lastWall
	c.lastCPU, c.lastWall = used, now
	wall := now.Sub(prevWall)
	if prevWall.IsZero() || wall <= 0 {
		return nil
	}
	pct := float64(used-prevCPU) / float64(wall) / float64(runtime.NumCPU()) * 100
	pct = min(max(pct, 0), 100)
	return &pct
}
