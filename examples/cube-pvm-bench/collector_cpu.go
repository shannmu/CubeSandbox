package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type CPUCollector struct {
	clkTck    float64
	prevTicks int64
	prevTime  time.Time
}

func (c *CPUCollector) Name() string { return "cpu_pct" }
func (c *CPUCollector) Unit() string { return "%" }

func (c *CPUCollector) Init(pids []string) {
	c.clkTck = float64(getClkTck())
	c.prevTicks = readCPUTicks(pids)
	c.prevTime = time.Now()
}

func (c *CPUCollector) Collect(pids []string) float64 {
	curTicks := readCPUTicks(pids)
	curTime := time.Now()
	dt := curTime.Sub(c.prevTime).Seconds()

	var pct float64
	if c.prevTicks >= 0 && curTicks >= 0 && dt > 0 {
		pct = float64(curTicks-c.prevTicks) / c.clkTck / dt * 100
	}

	c.prevTicks = curTicks
	c.prevTime = curTime
	return pct
}

func readCPUTicks(pids []string) int64 {
	var total int64
	found := false
	for _, pid := range pids {
		data, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
		if err != nil {
			continue
		}
		content := string(data)
		idx := strings.LastIndex(content, ")")
		if idx < 0 || idx+2 >= len(content) {
			continue
		}
		fields := strings.Fields(content[idx+2:])
		if len(fields) < 13 {
			continue
		}
		utime, err1 := strconv.ParseInt(fields[11], 10, 64)
		stime, err2 := strconv.ParseInt(fields[12], 10, 64)
		if err1 != nil || err2 != nil {
			continue
		}
		total += utime + stime
		found = true
	}
	if !found {
		return -1
	}
	return total
}

func getClkTck() int64 {
	out, err := exec.Command("getconf", "CLK_TCK").Output()
	if err != nil {
		return 100
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if err != nil || v <= 0 {
		return 100
	}
	return v
}
