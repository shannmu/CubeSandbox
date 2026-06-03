package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

type ProgressEvent struct {
	Suite    string
	Workload string
	Iter     int
	Total    int
	Value    float64
	Unit     string
	Err      string
	Done     bool
}

func RunAll(ctx context.Context, cfg *Config, suites []Suite, progressCh chan<- ProgressEvent) *BenchmarkRun {
	startTime := time.Now()

	run := &BenchmarkRun{
		Version:   1,
		Timestamp: startTime,
		Label:     cfg.Label,
		Config: RunConfig{
			Template:    cfg.Template,
			APIURL:      cfg.APIURL,
			Iterations:  cfg.Iterations,
			Warmup:      cfg.Warmup,
			Concurrency: cfg.Concurrency,
			Suites:      cfg.Suites,
		},
		Suites: make(map[string]SuiteResult),
	}

	hostname, _ := os.Hostname()
	run.Environment = EnvironmentInfo{
		Hostname:  hostname,
		GoVersion: runtime.Version(),
	}

	if cfg.DryRun {
		runDry(ctx, cfg, suites, run, progressCh)
	} else {
		runReal(ctx, cfg, suites, run, progressCh)
	}

	run.Summary = RunSummary{
		TotalDuration: time.Since(startTime).Seconds(),
		SuitesRun:     len(run.Suites),
	}
	for _, sr := range run.Suites {
		for _, wr := range sr.Workloads {
			run.Summary.WorkloadsRun++
			run.Summary.ErrorCount += len(wr.Errors)
		}
	}

	if progressCh != nil {
		progressCh <- ProgressEvent{Done: true}
	}
	return run
}

func runReal(ctx context.Context, cfg *Config, suites []Suite, run *BenchmarkRun, progressCh chan<- ProgressEvent) {
	sdkCfg := cubesandbox.Config{
		APIURL:     cfg.APIURL,
		APIKey:     cfg.APIKey,
		TemplateID: cfg.Template,
		Timeout:    cfg.Timeout,
	}
	client := cubesandbox.NewClient(sdkCfg)
	defer client.Close()

	hasNonLifecycle := false
	for _, s := range suites {
		if s.Name != "lifecycle" {
			hasNonLifecycle = true
			break
		}
	}

	var sandbox *cubesandbox.Sandbox
	if hasNonLifecycle {
		var err error
		sandbox, err = client.Create(ctx, cubesandbox.CreateOptions{})
		if err != nil {
			if progressCh != nil {
				progressCh <- ProgressEvent{Err: fmt.Sprintf("failed to create sandbox: %v", err)}
			}
			return
		}
		defer sandbox.Kill(ctx)

		info, err := sandbox.GetInfo(ctx)
		if err == nil {
			run.Environment.SandboxCPU = info.CPUCount
			run.Environment.SandboxMem = info.MemoryMB
		}
	}

	for _, suite := range suites {
		if suite.Name == "lifecycle" {
			sr := runLifecycleSuite(ctx, cfg, client, progressCh)
			run.Suites[suite.Name] = sr
			continue
		}

		suiteStart := time.Now()
		var workloadResults []WorkloadResult

		for _, wl := range suite.Workloads {
			wr := runWorkload(ctx, cfg, sandbox, wl, progressCh)
			workloadResults = append(workloadResults, wr)
		}

		run.Suites[suite.Name] = SuiteResult{
			Name:      suite.Name,
			Workloads: workloadResults,
			Duration:  time.Since(suiteStart).Seconds(),
		}
	}
}

func runWorkload(ctx context.Context, cfg *Config, sandbox *cubesandbox.Sandbox, wl Workload, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{
		Name:           wl.Name,
		Unit:           wl.Unit,
		HigherIsBetter: wl.HigherIsBetter,
	}

	totalIter := cfg.Warmup + cfg.Iterations
	for i := 0; i < totalIter; i++ {
		result, err := sandbox.Commands().Run(ctx, wl.Command, cubesandbox.CommandOptions{
			Timeout: cfg.Timeout,
		})
		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}

		if result.ExitCode != 0 {
			if i >= cfg.Warmup {
				errMsg := fmt.Sprintf("exit %d: %s", result.ExitCode, strings.TrimSpace(result.Stderr))
				wr.Errors = append(wr.Errors, errMsg)
			}
			continue
		}

		val, err := wl.ParseResult(result.Stdout, result.Stderr)
		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}

		if i >= cfg.Warmup {
			wr.Samples = append(wr.Samples, val)
			if progressCh != nil {
				progressCh <- ProgressEvent{
					Suite:    wl.Suite,
					Workload: wl.Name,
					Iter:     i - cfg.Warmup + 1,
					Total:    cfg.Iterations,
					Value:    val,
					Unit:     wl.Unit,
				}
			}
		}
	}

	if len(wr.Samples) > 0 {
		wr.Stats = computeStats(wr.Samples)
	}
	return wr
}

func runLifecycleSuite(ctx context.Context, cfg *Config, client *cubesandbox.Client, progressCh chan<- ProgressEvent) SuiteResult {
	suiteStart := time.Now()
	var workloadResults []WorkloadResult

	// Create benchmark
	createResult := benchmarkCreate(ctx, cfg, client, progressCh)
	workloadResults = append(workloadResults, createResult)

	// Snapshot benchmark
	snapshotResult := benchmarkSnapshot(ctx, cfg, client, progressCh)
	workloadResults = append(workloadResults, snapshotResult)

	// Rollback benchmark
	rollbackResult := benchmarkRollback(ctx, cfg, client, progressCh)
	workloadResults = append(workloadResults, rollbackResult)

	// Clone benchmark
	cloneResult := benchmarkClone(ctx, cfg, client, progressCh)
	workloadResults = append(workloadResults, cloneResult)

	return SuiteResult{
		Name:      "lifecycle",
		Workloads: workloadResults,
		Duration:  time.Since(suiteStart).Seconds(),
	}
}

func benchmarkCreate(ctx context.Context, cfg *Config, client *cubesandbox.Client, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{Name: "create", Unit: "ms", HigherIsBetter: false}

	for i := 0; i < cfg.Warmup+cfg.Iterations; i++ {
		t0 := time.Now()
		sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{})
		elapsed := float64(time.Since(t0).Microseconds()) / 1000.0

		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}
		sandbox.Kill(ctx)

		if i >= cfg.Warmup {
			wr.Samples = append(wr.Samples, elapsed)
			if progressCh != nil {
				progressCh <- ProgressEvent{
					Suite: "lifecycle", Workload: "create",
					Iter: i - cfg.Warmup + 1, Total: cfg.Iterations,
					Value: elapsed, Unit: "ms",
				}
			}
		}
	}

	if len(wr.Samples) > 0 {
		wr.Stats = computeStats(wr.Samples)
	}
	return wr
}

func benchmarkSnapshot(ctx context.Context, cfg *Config, client *cubesandbox.Client, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{Name: "snapshot", Unit: "ms", HigherIsBetter: false}

	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{})
	if err != nil {
		wr.Errors = append(wr.Errors, fmt.Sprintf("setup: %v", err))
		return wr
	}
	defer sandbox.Kill(ctx)

	for i := 0; i < cfg.Warmup+cfg.Iterations; i++ {
		t0 := time.Now()
		err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", nil)
		elapsed := float64(time.Since(t0).Microseconds()) / 1000.0

		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}

		if i >= cfg.Warmup {
			wr.Samples = append(wr.Samples, elapsed)
			if progressCh != nil {
				progressCh <- ProgressEvent{
					Suite: "lifecycle", Workload: "snapshot",
					Iter: i - cfg.Warmup + 1, Total: cfg.Iterations,
					Value: elapsed, Unit: "ms",
				}
			}
		}
	}

	if len(wr.Samples) > 0 {
		wr.Stats = computeStats(wr.Samples)
	}
	return wr
}

func benchmarkRollback(ctx context.Context, cfg *Config, client *cubesandbox.Client, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{Name: "rollback", Unit: "ms", HigherIsBetter: false}

	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{})
	if err != nil {
		wr.Errors = append(wr.Errors, fmt.Sprintf("setup: %v", err))
		return wr
	}
	defer sandbox.Kill(ctx)

	// Take a snapshot first
	if err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", nil); err != nil {
		wr.Errors = append(wr.Errors, fmt.Sprintf("snapshot setup: %v", err))
		return wr
	}

	for i := 0; i < cfg.Warmup+cfg.Iterations; i++ {
		t0 := time.Now()
		err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/rollback", nil)
		elapsed := float64(time.Since(t0).Microseconds()) / 1000.0

		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}

		if i >= cfg.Warmup {
			wr.Samples = append(wr.Samples, elapsed)
			if progressCh != nil {
				progressCh <- ProgressEvent{
					Suite: "lifecycle", Workload: "rollback",
					Iter: i - cfg.Warmup + 1, Total: cfg.Iterations,
					Value: elapsed, Unit: "ms",
				}
			}
		}
	}

	if len(wr.Samples) > 0 {
		wr.Stats = computeStats(wr.Samples)
	}
	return wr
}

func benchmarkClone(ctx context.Context, cfg *Config, client *cubesandbox.Client, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{Name: "clone", Unit: "ms", HigherIsBetter: false}

	sandbox, err := client.Create(ctx, cubesandbox.CreateOptions{})
	if err != nil {
		wr.Errors = append(wr.Errors, fmt.Sprintf("setup: %v", err))
		return wr
	}
	defer sandbox.Kill(ctx)

	for i := 0; i < cfg.Warmup+cfg.Iterations; i++ {
		t0 := time.Now()
		// Snapshot + create from snapshot
		snapErr := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", nil)
		if snapErr != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, fmt.Sprintf("snapshot: %v", snapErr))
			}
			continue
		}
		clone, cloneErr := client.Create(ctx, cubesandbox.CreateOptions{})
		elapsed := float64(time.Since(t0).Microseconds()) / 1000.0
		if cloneErr != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, fmt.Sprintf("clone create: %v", cloneErr))
			}
			continue
		}
		clone.Kill(ctx)

		if i >= cfg.Warmup {
			wr.Samples = append(wr.Samples, elapsed)
			if progressCh != nil {
				progressCh <- ProgressEvent{
					Suite: "lifecycle", Workload: "clone",
					Iter: i - cfg.Warmup + 1, Total: cfg.Iterations,
					Value: elapsed, Unit: "ms",
				}
			}
		}
	}

	if len(wr.Samples) > 0 {
		wr.Stats = computeStats(wr.Samples)
	}
	return wr
}

func doHTTP(ctx context.Context, cfg *Config, method, path string, body any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, cfg.APIURL+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 400 {
		return fmt.Errorf("HTTP %d on %s %s", resp.StatusCode, method, path)
	}
	return nil
}

// --- Dry-run mode ---

func runDry(_ context.Context, cfg *Config, suites []Suite, run *BenchmarkRun, progressCh chan<- ProgressEvent) {
	run.Environment.SandboxCPU = 2
	run.Environment.SandboxMem = 512

	for _, suite := range suites {
		suiteStart := time.Now()
		var workloadResults []WorkloadResult

		for _, wl := range suite.Workloads {
			wr := WorkloadResult{
				Name:           wl.Name,
				Unit:           wl.Unit,
				HigherIsBetter: wl.HigherIsBetter,
			}

			for i := 0; i < cfg.Iterations; i++ {
				val := dryValue(wl)
				wr.Samples = append(wr.Samples, val)
				time.Sleep(50 * time.Millisecond)

				if progressCh != nil {
					progressCh <- ProgressEvent{
						Suite:    wl.Suite,
						Workload: wl.Name,
						Iter:     i + 1,
						Total:    cfg.Iterations,
						Value:    val,
						Unit:     wl.Unit,
					}
				}
			}

			wr.Stats = computeStats(wr.Samples)
			workloadResults = append(workloadResults, wr)
		}

		run.Suites[suite.Name] = SuiteResult{
			Name:      suite.Name,
			Workloads: workloadResults,
			Duration:  time.Since(suiteStart).Seconds(),
		}
	}
}

var dryBaselines = map[string]float64{
	"integer-arith":        45.0,
	"float-matrix":         28.0,
	"prime-sieve":          850.0,
	"seq-bandwidth":        2.5,
	"random-latency":       120.0,
	"page-fault":           380.0,
	"seq-write":            450.0,
	"seq-read":             1200.0,
	"random-4k":            15000.0,
	"fsync-latency":        250.0,
	"loopback-throughput":  8.5,
	"gateway-ping":         0.3,
	"getpid-loop":          180.0,
	"mmap-cycle":           12.0,
	"fork-exec":            4.5,
	"create":               65.0,
	"snapshot":             45.0,
	"rollback":             70.0,
	"clone":                130.0,
}

func dryValue(wl Workload) float64 {
	base, ok := dryBaselines[wl.Name]
	if !ok {
		base = 100.0
	}
	jitter := (rand.Float64() - 0.5) * 0.1 * base
	return base + jitter
}

