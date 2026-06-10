package main

import (
	"flag"
	"os"
	"strings"
	"time"

	"golang.org/x/term"
)

type Config struct {
	Suites      []string
	Iterations  int
	Warmup      int
	Concurrency int
	Output      string
	Baseline    string
	Label       string
	Template    string
	APIURL      string
	APIKey      string
	Timeout     time.Duration
	ThemeName   string
	NoTUI       bool
	JSONOnly    bool
	Verbose     bool
	DryRun         bool
	Local          bool
	Observe        bool
	IperfServerIP   string
	IperfServerPort string
}

var allSuites = []string{"cpu", "memory", "disk", "network", "syscall", "lifecycle"}

func parseConfig() *Config {
	cfg := &Config{}

	var suiteStr string
	flag.StringVar(&suiteStr, "s", "all", "Suites to run: cpu,memory,disk,network,syscall,lifecycle (comma-sep or 'all')")
	flag.StringVar(&suiteStr, "suite", "all", "Suites to run")
	flag.IntVar(&cfg.Iterations, "n", 5, "Iterations per workload")
	flag.IntVar(&cfg.Iterations, "iterations", 5, "Iterations per workload")
	flag.IntVar(&cfg.Warmup, "w", 1, "Warmup iterations (discarded)")
	flag.IntVar(&cfg.Warmup, "warmup", 1, "Warmup iterations")
	flag.IntVar(&cfg.Concurrency, "c", 5, "Concurrency for lifecycle benchmarks")
	flag.IntVar(&cfg.Concurrency, "concurrency", 5, "Concurrency for lifecycle benchmarks")
	flag.StringVar(&cfg.Output, "o", "", "Export JSON results to file (default: pvm.json or ecs.json)")
	flag.StringVar(&cfg.Output, "output", "", "Export JSON results to file")
	flag.StringVar(&cfg.Baseline, "b", "", "Load prior run JSON for comparison")
	flag.StringVar(&cfg.Baseline, "baseline", "", "Load prior run JSON for comparison")
	flag.StringVar(&cfg.Label, "l", "", "Label this run (e.g. 'pvm', 'kvm-native')")
	flag.StringVar(&cfg.Label, "label", "", "Label this run")
	flag.StringVar(&cfg.Template, "t", "", "Template ID (overrides CUBE_TEMPLATE_ID)")
	flag.StringVar(&cfg.Template, "template", "", "Template ID")
	flag.StringVar(&cfg.APIURL, "api-url", "", "CubeAPI URL (overrides CUBE_API_URL/E2B_API_URL)")
	flag.StringVar(&cfg.APIKey, "api-key", "", "API key (overrides CUBE_API_KEY/E2B_API_KEY)")
	flag.DurationVar(&cfg.Timeout, "timeout", 120*time.Second, "Per-command timeout")
	flag.StringVar(&cfg.ThemeName, "theme", "auto", "Color theme: dark|light|auto")
	flag.BoolVar(&cfg.NoTUI, "no-tui", false, "Disable interactive TUI")
	flag.BoolVar(&cfg.JSONOnly, "json", false, "Output JSON only to stdout")
	flag.BoolVar(&cfg.Verbose, "verbose", false, "Show raw command output")
	flag.BoolVar(&cfg.DryRun, "dry-run", false, "Simulate workloads with synthetic data")
	flag.BoolVar(&cfg.Local, "local", false, "Run benchmarks locally (for ECS baseline) instead of in a sandbox")
	flag.BoolVar(&cfg.Observe, "observe", false, "Collect host-side RSS/CPU metrics during workload execution")
	flag.StringVar(&cfg.IperfServerIP, "iperf-server-ip", "", "iperf3 server IP (overrides IPERF3_SERVER_IP)")
	flag.StringVar(&cfg.IperfServerPort, "iperf-server-port", "", "iperf3 server port (overrides IPERF3_SERVER_PORT, default: 5201)")

	flag.Parse()

	if cfg.JSONOnly {
		cfg.NoTUI = true
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		cfg.NoTUI = true
	}

	if suiteStr == "all" || suiteStr == "" {
		cfg.Suites = allSuites
	} else {
		cfg.Suites = strings.Split(suiteStr, ",")
		for i := range cfg.Suites {
			cfg.Suites[i] = strings.TrimSpace(cfg.Suites[i])
		}
	}

	if cfg.DryRun {
		if cfg.Template == "" {
			cfg.Template = "dry-run-template"
		}
		if cfg.APIURL == "" {
			cfg.APIURL = "http://localhost:3000"
		}
		if cfg.APIKey == "" {
			cfg.APIKey = "dry-run"
		}
	} else if cfg.Local {
		// Local mode: no SDK credentials needed, filter out lifecycle suite
		filtered := cfg.Suites[:0]
		for _, s := range cfg.Suites {
			if s != "lifecycle" {
				filtered = append(filtered, s)
			}
		}
		cfg.Suites = filtered
	} else {
		if cfg.Template == "" {
			cfg.Template = firstEnv("CUBE_TEMPLATE_ID")
		}
		if cfg.APIURL == "" {
			cfg.APIURL = strings.TrimRight(firstEnv("CUBE_API_URL", "E2B_API_URL"), "/")
		}
		if cfg.APIKey == "" {
			cfg.APIKey = firstEnv("CUBE_API_KEY", "E2B_API_KEY")
		}
	}

	if cfg.IperfServerIP == "" {
		cfg.IperfServerIP = firstEnv("IPERF3_SERVER_IP")
	}
	if cfg.IperfServerPort == "" {
		cfg.IperfServerPort = firstEnv("IPERF3_SERVER_PORT")
	}
	if cfg.IperfServerPort == "" {
		cfg.IperfServerPort = "5201"
	}

	if cfg.Output == "" {
		if cfg.Local {
			cfg.Output = "ecs.json"
		} else {
			cfg.Output = "pvm.json"
		}
	}

	if cfg.Iterations < 1 {
		cfg.Iterations = 1
	}
	if cfg.Concurrency < 1 {
		cfg.Concurrency = 1
	}

	return cfg
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return v
		}
	}
	return ""
}
