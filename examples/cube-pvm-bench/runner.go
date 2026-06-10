package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
	} else if cfg.Local {
		runLocal(ctx, cfg, suites, run, progressCh)
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
	sdkCfg := cubesandbox.NewConfigFromEnv()
	if cfg.APIURL != "" {
		sdkCfg.APIURL = cfg.APIURL
	}
	if cfg.APIKey != "" {
		sdkCfg.APIKey = cfg.APIKey
	}
	if cfg.Template != "" {
		sdkCfg.TemplateID = cfg.Template
	}
	if cfg.Timeout > 0 {
		sdkCfg.Timeout = cfg.Timeout
	}
	client := cubesandbox.NewClient(sdkCfg)
	defer client.Close()

	if cfg.Verbose {
		fmt.Fprintf(os.Stderr, "[diag] SDK config: APIURL=%s ProxyNodeIP=%s ProxyPort=%d ProxyScheme=%s SandboxDomain=%s\n",
			sdkCfg.APIURL, sdkCfg.ProxyNodeIP, sdkCfg.ProxyPortHTTP, sdkCfg.ProxyScheme, sdkCfg.SandboxDomain)
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
			createOpts := cubesandbox.CreateOptions{}
			if wl.Suite == "network" && cfg.IperfServerIP != "" {
				allowInternet := true
				createOpts.AllowInternetAccess = &allowInternet
				createOpts.Network = cubesandbox.NetworkOptions{
					AllowOut: []string{cfg.IperfServerIP + "/32"},
				}
			}
			sandbox, err := client.Create(ctx, createOpts)
			if err != nil {
				if progressCh != nil {
					progressCh <- ProgressEvent{Err: fmt.Sprintf("failed to create sandbox for %s/%s: %v", wl.Suite, wl.Name, err)}
				}
				workloadResults = append(workloadResults, WorkloadResult{
					Name:           wl.Name,
					Unit:           wl.Unit,
					HigherIsBetter: wl.HigherIsBetter,
					Errors:         []string{fmt.Sprintf("sandbox create: %v", err)},
				})
				continue
			}

			if run.Environment.SandboxCPU == 0 {
				if info, err := sandbox.GetInfo(ctx); err == nil {
					run.Environment.SandboxCPU = info.CPUCount
					run.Environment.SandboxMem = info.MemoryMB
				}
			}

			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[diag] sandbox for %s/%s: ID=%s\n", wl.Suite, wl.Name, sandbox.SandboxID)
			}

			var wr WorkloadResult
			if cfg.Observe {
				collectors := DefaultCollectors()
				stop, obsCh := StartObserver(ctx, sandbox.SandboxID, time.Second, collectors)
				wr = runWorkload(ctx, cfg, sandbox, wl, progressCh)
				stop()
				wr.Observations = <-obsCh
				if cfg.Verbose && wr.Observations != nil {
					fmt.Fprintf(os.Stderr, "[observe] %s/%s: %s\n", wl.Suite, wl.Name, formatObservations(wr.Observations))
				}
			} else {
				wr = runWorkload(ctx, cfg, sandbox, wl, progressCh)
			}
			sandbox.Kill(ctx)
			workloadResults = append(workloadResults, wr)
		}

		run.Suites[suite.Name] = SuiteResult{
			Name:      suite.Name,
			Workloads: workloadResults,
			Duration:  time.Since(suiteStart).Seconds(),
		}
	}
}

func wrapCommand(cmd string) string {
	encoded := base64.StdEncoding.EncodeToString([]byte(cmd))
	return fmt.Sprintf(`import subprocess,sys,base64
cmd = base64.b64decode("%s").decode()
r = subprocess.run(cmd, shell=True, capture_output=True, text=True)
sys.stdout.write(r.stdout)
if r.stderr:
    sys.stderr.write(r.stderr)
if r.returncode != 0:
    raise RuntimeError(f"exit {r.returncode}: {r.stderr.strip()}")
`, encoded)
}

func runWorkload(ctx context.Context, cfg *Config, sandbox *cubesandbox.Sandbox, wl Workload, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{
		Name:           wl.Name,
		Unit:           wl.Unit,
		HigherIsBetter: wl.HigherIsBetter,
	}

	code := wrapCommand(wl.Command)
	totalIter := cfg.Warmup + cfg.Iterations
	for i := 0; i < totalIter; i++ {
		exec, err := sandbox.RunCode(ctx, code, cubesandbox.RunCodeOptions{
			Timeout: cfg.Timeout,
		})
		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, err.Error())
			}
			continue
		}

		if exec.Error != nil {
			if i >= cfg.Warmup {
				errMsg := fmt.Sprintf("%s: %s", exec.Error.Name, exec.Error.Value)
				wr.Errors = append(wr.Errors, errMsg)
			}
			continue
		}

		stdout := strings.Join(exec.Logs.Stdout, "")
		stderr := strings.Join(exec.Logs.Stderr, "")
		val, err := wl.ParseResult(stdout, stderr)
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
		err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", map[string]any{})
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
	if err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", map[string]any{}); err != nil {
		wr.Errors = append(wr.Errors, fmt.Sprintf("snapshot setup: %v", err))
		return wr
	}

	for i := 0; i < cfg.Warmup+cfg.Iterations; i++ {
		t0 := time.Now()
		err := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/rollback", map[string]any{})
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
		snapErr := doHTTP(ctx, cfg, "POST", "/sandboxes/"+url.PathEscape(sandbox.SandboxID)+"/snapshots", map[string]any{})
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

// --- Local mode (ECS baseline) ---

func runLocal(ctx context.Context, cfg *Config, suites []Suite, run *BenchmarkRun, progressCh chan<- ProgressEvent) {
	run.Environment.SandboxCPU = runtime.NumCPU()
	run.Environment.SandboxMem = localMemoryMB()

	for _, suite := range suites {
		suiteStart := time.Now()
		var workloadResults []WorkloadResult

		for _, wl := range suite.Workloads {
			if cfg.Verbose {
				fmt.Fprintf(os.Stderr, "[local] running %s/%s\n", wl.Suite, wl.Name)
			}
			wr := runWorkloadLocal(ctx, cfg, wl, progressCh)
			workloadResults = append(workloadResults, wr)
		}

		run.Suites[suite.Name] = SuiteResult{
			Name:      suite.Name,
			Workloads: workloadResults,
			Duration:  time.Since(suiteStart).Seconds(),
		}
	}
}

func runWorkloadLocal(ctx context.Context, cfg *Config, wl Workload, progressCh chan<- ProgressEvent) WorkloadResult {
	wr := WorkloadResult{
		Name:           wl.Name,
		Unit:           wl.Unit,
		HigherIsBetter: wl.HigherIsBetter,
	}

	totalIter := cfg.Warmup + cfg.Iterations
	for i := 0; i < totalIter; i++ {
		cmdCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		cmd := exec.CommandContext(cmdCtx, "bash", "-c", wl.Command)
		var stdoutBuf, stderrBuf bytes.Buffer
		cmd.Stdout = &stdoutBuf
		cmd.Stderr = &stderrBuf
		err := cmd.Run()
		cancel()

		stdout := stdoutBuf.String()
		stderr := stderrBuf.String()

		if cfg.Verbose {
			fmt.Fprintf(os.Stderr, "[local] %s iter=%d stdout=%d bytes stderr=%d bytes err=%v\n",
				wl.Name, i, len(stdout), len(stderr), err)
		}

		if err != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, fmt.Sprintf("exec: %v (stderr: %s)", err, strings.TrimSpace(stderr)))
			}
			continue
		}

		val, parseErr := wl.ParseResult(stdout, stderr)
		if parseErr != nil {
			if i >= cfg.Warmup {
				wr.Errors = append(wr.Errors, parseErr.Error())
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

func localMemoryMB() int {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.Atoi(fields[1])
				return kb / 1024
			}
		}
	}
	return 0
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
	// cpu
	"cpu-int-add":        0.8,
	"cpu-int-div":        5.0,
	"cpu-double-add":     1.2,
	"cpu-aes-1t":         900.0,
	"cpu-aes-mt":         3200.0,
	"cpu-sha256-1t":      600.0,
	"cpu-sha256-mt":      2100.0,
	"cpu-ipc-unix-lat":   8.0,
	"cpu-ipc-unix-bw":    5000.0,
	"cpu-compress-gzip":  80.0,
	// memory
	"mem-bandwidth-rd": 12000.0,
	"mem-bandwidth-wr": 8000.0,
	"mem-bandwidth-cp": 6000.0,
	"mem-latency":      80.0,
	"mem-sysbench-rd":  5000.0,
	"mem-sysbench-wr":  3500.0,
	// memory (lmbench additions)
	"lat-mmap":      15.0,
	"lat-pagefault": 3.0,
	"bw-mmap-rd":    10000.0,
	// disk (fio)
	"seq-write":    450.0,
	"seq-read":     1200.0,
	"rand-read-4k":      15000.0,
	"rand-write-4k":     8000.0,
	"fsync-latency":     250.0,
	"mixed-randrw":      20000.0,
	"small-file-create": 1500.0,
	// network
	"tcp-stream":   25.0,
	"tcp-parallel": 40.0,
	"udp-pps":     50000.0,
	"lat-connect": 30.0,
	// syscall (lmbench, microseconds)
	"lat-syscall-null":  0.1,
	"lat-syscall-read":  0.15,
	"lat-syscall-write": 0.15,
	"lat-proc-fork":     100.0,
	"lat-proc-exec":     300.0,
	"lat-ctx":           3.0,
	"lat-select":        8.0,
	// lifecycle
	"create":   65.0,
	"snapshot": 45.0,
	"clone":    130.0,
}

func dryValue(wl Workload) float64 {
	base, ok := dryBaselines[wl.Name]
	if !ok {
		base = 100.0
	}
	jitter := (rand.Float64() - 0.5) * 0.1 * base
	return base + jitter
}

