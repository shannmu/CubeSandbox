package main

import "fmt"

func networkWorkloads(serverIP, serverPort string) []Workload {
	return []Workload{
		{
			Name:           "tcp-stream",
			Suite:          "network",
			Description:    fmt.Sprintf("iperf3 TCP single-stream to %s:%s, 15s", serverIP, serverPort),
			Unit:           "Gbps",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`iperf3 -c %s -p %s -t 15 -J`, serverIP, serverPort),
			ParseResult:    parseIperf3,
		},
		{
			Name:           "tcp-parallel",
			Suite:          "network",
			Description:    fmt.Sprintf("iperf3 TCP 4-stream parallel to %s:%s, 15s", serverIP, serverPort),
			Unit:           "Gbps",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`iperf3 -c %s -p %s -t 15 -P 4 -J`, serverIP, serverPort),
			ParseResult:    parseIperf3,
		},
		{
			Name:           "udp-pps",
			Suite:          "network",
			Description:    fmt.Sprintf("iperf3 UDP 256-byte small packet PPS to %s:%s, 15s", serverIP, serverPort),
			Unit:           "pps",
			HigherIsBetter: true,
			Command:        fmt.Sprintf(`iperf3 -c %s -p %s -u -l 256 -b 0 -t 15 -J`, serverIP, serverPort),
			ParseResult:    parseIperf3PPS,
		},
		{
			Name:           "lat-connect",
			Suite:          "network",
			Description:    fmt.Sprintf("lmbench lat_connect: TCP connection latency to %s, 200 iterations", serverIP),
			Unit:           "us",
			HigherIsBetter: false,
			Command:        fmt.Sprintf(`%s/lat_connect -N 200 %s 2>&1`, lmbenchBinDir, serverIP),
			ParseResult:    parseLmbenchMicroseconds,
		},
	}
}
