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
	DryRun      bool
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
	flag.StringVar(&cfg.Output, "o", "", "Export JSON results to file")
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
