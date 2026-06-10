package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const lmbenchBinDir = "/usr/lib/lmbench/bin/x86_64-linux-gnu"

type Workload struct {
	Name           string
	Suite          string
	Description    string
	Command        string
	ParseResult    func(stdout, stderr string) (float64, error)
	Unit           string
	HigherIsBetter bool
}

type Suite struct {
	Name      string
	Workloads []Workload
}

func buildRegistry(cfg *Config) map[string]Suite {
	return map[string]Suite{
		"cpu":       {Name: "cpu", Workloads: cpuWorkloads()},
		"memory":    {Name: "memory", Workloads: memoryWorkloads()},
		"disk":      {Name: "disk", Workloads: diskWorkloads()},
		"network":   {Name: "network", Workloads: networkWorkloads(cfg.IperfServerIP, cfg.IperfServerPort)},
		"syscall":   {Name: "syscall", Workloads: syscallWorkloads()},
		"lifecycle": {Name: "lifecycle", Workloads: lifecycleWorkloads()},
	}
}

func getSuites(names []string, registry map[string]Suite) ([]Suite, error) {
	var suites []Suite
	for _, name := range names {
		s, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("unknown suite: %q (available: %s)", name, strings.Join(allSuites, ", "))
		}
		suites = append(suites, s)
	}
	return suites, nil
}

func parseFloat(stdout, _ string) (float64, error) {
	s := strings.TrimSpace(stdout)
	lines := strings.Split(s, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	v, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", last, err)
	}
	return v, nil
}

// --- sysbench parsers ---

var sysbenchEventsRe = regexp.MustCompile(`events per second:\s+([\d.]+)`)

func parseSysbenchCPU(stdout, _ string) (float64, error) {
	m := sysbenchEventsRe.FindStringSubmatch(stdout)
	if m == nil {
		return 0, fmt.Errorf("cannot find 'events per second' in sysbench output")
	}
	return strconv.ParseFloat(m[1], 64)
}

var sysbenchMemThroughputRe = regexp.MustCompile(`([\d.]+)\s+MiB/sec`)

func parseSysbenchMem(stdout, _ string) (float64, error) {
	m := sysbenchMemThroughputRe.FindStringSubmatch(stdout)
	if m == nil {
		return 0, fmt.Errorf("cannot find MiB/sec in sysbench memory output")
	}
	return strconv.ParseFloat(m[1], 64)
}

// --- lmbench parsers ---

func parseLmbenchBW(stdout, _ string) (float64, error) {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			v, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("cannot parse lmbench bandwidth output")
}

func parseLmbenchLatency(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	lines := strings.Split(strings.TrimSpace(combined), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			v, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				return v, nil
			}
		}
		if len(fields) == 1 {
			v, err := strconv.ParseFloat(fields[0], 64)
			if err == nil {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("cannot parse lmbench latency output")
}

var lmbenchUsecRe = regexp.MustCompile(`([\d.]+)\s*microseconds`)

func parseLmbenchMicroseconds(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	m := lmbenchUsecRe.FindStringSubmatch(combined)
	if m != nil {
		return strconv.ParseFloat(m[1], 64)
	}
	return parseLmbenchLatency(stdout, stderr)
}

func parseLmbenchCtx(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	lines := strings.Split(strings.TrimSpace(combined), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "\"") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			v, err := strconv.ParseFloat(fields[1], 64)
			if err == nil {
				return v, nil
			}
		}
	}
	return 0, fmt.Errorf("cannot parse lmbench context switch output")
}

// --- fio JSON parsers ---

type fioOutput struct {
	Jobs []fioJob `json:"jobs"`
}

type fioJob struct {
	Read  fioRW    `json:"read"`
	Write fioRW    `json:"write"`
	Sync  fioSync  `json:"sync"`
}

type fioRW struct {
	BWBytes  float64    `json:"bw_bytes"`
	BW       float64    `json:"bw"`
	IOPS     float64    `json:"iops"`
	LatNS    fioLatency `json:"lat_ns"`
}

type fioSync struct {
	LatNS fioLatency `json:"lat_ns"`
}

type fioLatency struct {
	Mean       float64            `json:"mean"`
	Percentile map[string]float64 `json:"percentile"`
}

func parseFioJSON(stdout string) (*fioOutput, error) {
	var out fioOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return nil, fmt.Errorf("parse fio JSON: %w", err)
	}
	if len(out.Jobs) == 0 {
		return nil, fmt.Errorf("fio output has no jobs")
	}
	return &out, nil
}

func parseFioReadBW(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Read.BWBytes / 1024 / 1024, nil
}

func parseFioWriteBW(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Write.BWBytes / 1024 / 1024, nil
}

func parseFioReadIOPS(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Read.IOPS, nil
}

func parseFioWriteIOPS(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Write.IOPS, nil
}

func parseFioSyncLatUs(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Sync.LatNS.Mean / 1000, nil
}

func parseFioMixedIOPS(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	return out.Jobs[0].Read.IOPS + out.Jobs[0].Write.IOPS, nil
}

func parseFioSeqRW(stdout, _ string) (float64, error) {
	out, err := parseFioJSON(stdout)
	if err != nil {
		return 0, err
	}
	if len(out.Jobs) < 2 {
		return 0, fmt.Errorf("fio seq-rw: expected 2 jobs, got %d", len(out.Jobs))
	}
	writeBW := out.Jobs[0].Write.BWBytes / 1024 / 1024
	readBW := out.Jobs[1].Read.BWBytes / 1024 / 1024
	return (writeBW + readBW) / 2, nil
}

// --- iperf3 JSON parser ---

type iperf3Output struct {
	End iperf3End `json:"end"`
}

type iperf3End struct {
	SumReceived iperf3Sum `json:"sum_received"`
	Sum         iperf3Sum `json:"sum"`
}

type iperf3Sum struct {
	BitsPerSecond float64 `json:"bits_per_second"`
	Packets       float64 `json:"packets"`
	LostPackets   float64 `json:"lost_packets"`
	Seconds       float64 `json:"seconds"`
}

func parseIperf3(stdout, _ string) (float64, error) {
	var out iperf3Output
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return 0, fmt.Errorf("parse iperf3 JSON: %w", err)
	}
	gbps := out.End.SumReceived.BitsPerSecond / 1e9
	return gbps, nil
}

func parseIperf3PPS(stdout, _ string) (float64, error) {
	var out iperf3Output
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		return 0, fmt.Errorf("parse iperf3 JSON: %w", err)
	}
	s := out.End.Sum
	if s.Seconds <= 0 || s.Packets <= 0 {
		return 0, fmt.Errorf("iperf3 UDP: no valid stats")
	}
	received := s.Packets - s.LostPackets
	return received / s.Seconds, nil
}

// --- ping parser (kept for gateway-ping) ---

var pingRttRe = regexp.MustCompile(`([\d.]+)/([\d.]+)/([\d.]+)/([\d.]+)`)

func parsePingOutput(stdout, stderr string) (float64, error) {
	combined := stdout + "\n" + stderr
	matches := pingRttRe.FindStringSubmatch(combined)
	if matches == nil {
		s := strings.TrimSpace(combined)
		v, err := strconv.ParseFloat(s, 64)
		if err == nil {
			return v, nil
		}
		return 0, fmt.Errorf("cannot parse ping output: %q", s)
	}
	avg, err := strconv.ParseFloat(matches[2], 64)
	if err != nil {
		return 0, err
	}
	return avg, nil
}
