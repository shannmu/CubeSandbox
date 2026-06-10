package main

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type RSSCollector struct{}

func (c *RSSCollector) Name() string { return "rss_mb" }
func (c *RSSCollector) Unit() string { return "MB" }
func (c *RSSCollector) Init(_ []string) {}

func (c *RSSCollector) Collect(pids []string) float64 {
	var totalKB int64
	for _, pid := range pids {
		f, err := os.Open(filepath.Join("/proc", pid, "status"))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					v, _ := strconv.ParseInt(fields[1], 10, 64)
					totalKB += v
				}
				break
			}
		}
		f.Close()
	}
	return float64(totalKB) / 1024.0
}
