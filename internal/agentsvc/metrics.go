package agentsvc

import (
	"bufio"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/israel-duff/pgdock/internal/agentapi"
)

// hostMetrics reads load, memory, and disk usage from /proc and statfs.
func hostMetrics(diskPath string) agentapi.HostMetrics {
	m := agentapi.HostMetrics{CPUs: runtime.NumCPU(), DiskPath: diskPath}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		f := strings.Fields(string(b))
		if len(f) >= 2 {
			m.Load1, _ = strconv.ParseFloat(f[0], 64)
			m.Load5, _ = strconv.ParseFloat(f[1], 64)
		}
	}
	if f, err := os.Open("/proc/meminfo"); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 2 {
				continue
			}
			kb, _ := strconv.ParseInt(fields[1], 10, 64)
			switch fields[0] {
			case "MemTotal:":
				m.MemTotalBytes = kb * 1024
			case "MemAvailable:":
				m.MemAvailableBytes = kb * 1024
			}
		}
		_ = f.Close()
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(diskPath, &st); err == nil {
		m.DiskTotalBytes = int64(st.Blocks) * int64(st.Bsize) //nolint:unconvert // Bsize is int32 on some platforms
		m.DiskFreeBytes = int64(st.Bavail) * int64(st.Bsize)  //nolint:unconvert // likewise
	}
	return m
}
