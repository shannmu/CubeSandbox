package main

import (
	"encoding/json"
	"os"
	"time"
)

type BenchmarkRun struct {
	Version     int                    `json:"version"`
	Timestamp   time.Time              `json:"timestamp"`
	Label       string                 `json:"label"`
	Config      RunConfig              `json:"config"`
	Environment EnvironmentInfo        `json:"environment"`
	Suites      map[string]SuiteResult `json:"suites"`
	Summary     RunSummary             `json:"summary"`
}

type RunConfig struct {
	Template    string   `json:"template"`
	APIURL      string   `json:"api_url"`
	Iterations  int      `json:"iterations"`
	Warmup      int      `json:"warmup"`
	Concurrency int      `json:"concurrency"`
	Suites      []string `json:"suites"`
}

type EnvironmentInfo struct {
	Hostname   string `json:"hostname"`
	GoVersion  string `json:"go_version"`
	SandboxCPU int    `json:"sandbox_cpu_count"`
	SandboxMem int    `json:"sandbox_memory_mb"`
}

type SuiteResult struct {
	Name      string           `json:"name"`
	Workloads []WorkloadResult `json:"workloads"`
	Duration  float64          `json:"duration_s"`
}

type WorkloadResult struct {
	Name           string        `json:"name"`
	Unit           string        `json:"unit"`
	HigherIsBetter bool          `json:"higher_is_better"`
	Samples        []float64     `json:"samples"`
	Stats          StatBlock     `json:"stats"`
	Errors         []string      `json:"errors,omitempty"`
	Observations   *Observations `json:"observations,omitempty"`
}

type StatBlock struct {
	Count  int     `json:"count"`
	Mean   float64 `json:"mean"`
	StdDev float64 `json:"stddev"`
	Min    float64 `json:"min"`
	Max    float64 `json:"max"`
	P50    float64 `json:"p50"`
	P90    float64 `json:"p90"`
	P95    float64 `json:"p95"`
	P99    float64 `json:"p99"`
	CV     float64 `json:"cv"`
}

type RunSummary struct {
	TotalDuration float64 `json:"total_duration_s"`
	SuitesRun     int     `json:"suites_run"`
	WorkloadsRun  int     `json:"workloads_run"`
	ErrorCount    int     `json:"errors"`
}

func saveJSON(run *BenchmarkRun, path string) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func loadBaseline(path string) (*BenchmarkRun, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var run BenchmarkRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}
