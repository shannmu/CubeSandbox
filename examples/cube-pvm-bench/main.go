package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const banner = `
   ██████╗██╗   ██╗██████╗ ███████╗    ██████╗ ██╗   ██╗███╗   ███╗
  ██╔════╝██║   ██║██╔══██╗██╔════╝    ██╔══██╗██║   ██║████╗ ████║
  ██║     ██║   ██║██████╔╝█████╗      ██████╔╝██║   ██║██╔████╔██║
  ██║     ██║   ██║██╔══██╗██╔══╝      ██╔═══╝ ╚██╗ ██╔╝██║╚██╔╝██║
  ╚██████╗╚██████╔╝██████╔╝███████╗    ██║      ╚████╔╝ ██║ ╚═╝ ██║
   ╚═════╝ ╚═════╝ ╚═════╝ ╚══════╝    ╚═╝       ╚═══╝  ╚═╝     ╚═╝
                         B E N C H M A R K`

func main() {
	cfg := parseConfig()

	switch cfg.ThemeName {
	case "light":
		T = LightTheme
	case "dark":
		T = DarkTheme
	default:
		T = DetectTheme()
	}

	if !cfg.DryRun {
		if cfg.Template == "" {
			fatal("template ID not set. Use -t or set CUBE_TEMPLATE_ID.")
		}
		if cfg.APIURL == "" {
			fatal("API URL not set. Use --api-url or set CUBE_API_URL.")
		}
		if cfg.APIKey == "" {
			fatal("API key not set. Use --api-key or set CUBE_API_KEY.")
		}
	}

	suites, err := getSuites(cfg.Suites)
	if err != nil {
		fatal(err.Error())
	}

	if !cfg.JSONOnly {
		renderBanner()
		renderConfigPanel(cfg)
		if cfg.DryRun {
			renderDryRunNotice()
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	totalWorkloads := 0
	for _, s := range suites {
		totalWorkloads += len(s.Workloads)
	}

	progressCh := make(chan ProgressEvent, totalWorkloads*cfg.Iterations)

	var run *BenchmarkRun

	if cfg.NoTUI {
		go func() {
			run = RunAll(ctx, cfg, suites, progressCh)
		}()
		collectSimpleProgress(progressCh, cfg)
	} else {
		go func() {
			run = RunAll(ctx, cfg, suites, progressCh)
		}()

		m := newUIModel(cfg, totalWorkloads, progressCh)
		p := tea.NewProgram(m)
		if _, err := p.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
			os.Exit(1)
		}
	}

	if run == nil {
		fatal("benchmark did not produce results")
	}

	// Load baseline for comparison
	var comparison *ComparisonResult
	if cfg.Baseline != "" {
		baseline, err := loadBaseline(cfg.Baseline)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot load baseline %s: %v\n", cfg.Baseline, err)
		} else {
			comparison = ComputeComparison(baseline, run)
		}
	}

	if cfg.JSONOnly {
		data, _ := json.MarshalIndent(run, "", "  ")
		fmt.Println(string(data))
	} else {
		RenderReport(run, cfg, comparison)
	}

	if cfg.Output != "" {
		if err := saveJSON(run, cfg.Output); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving JSON: %v\n", err)
		} else if !cfg.JSONOnly {
			fmt.Printf("  %s %s\n\n", T.Muted.Render("Results saved to"), lipgloss.NewStyle().Bold(true).Render(cfg.Output))
		}
	}

	if run.Summary.ErrorCount > 0 && !cfg.DryRun {
		os.Exit(1)
	}
}

func renderBanner() {
	styled := T.Banner.Render(banner)
	fmt.Println(lipgloss.PlaceHorizontal(80, lipgloss.Center, styled))
	fmt.Println()
}

func renderConfigPanel(cfg *Config) {
	kvs := []kvPair{
		{"Template", cfg.Template},
		{"API URL", cfg.APIURL},
		{"Suites", joinSuites(cfg.Suites)},
		{"Iterations", fmt.Sprintf("%d (warmup: %d)", cfg.Iterations, cfg.Warmup)},
		{"Concurrency", fmt.Sprintf("%d", cfg.Concurrency)},
		{"Timeout", cfg.Timeout.String()},
	}
	if cfg.Label != "" {
		kvs = append(kvs, kvPair{"Label", T.Accent.Render(cfg.Label)})
	}
	if cfg.Baseline != "" {
		kvs = append(kvs, kvPair{"Baseline", cfg.Baseline})
	}

	content := renderKV(kvs)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(T.Border).
		Padding(1, 3).
		Width(80).
		Render(T.Heading.Render("  Configuration") + "\n\n" + content)
	fmt.Println(box)
	fmt.Println()
}

func renderDryRunNotice() {
	content := fmt.Sprintf("  %s - using synthetic data, results are not real measurements",
		T.Warn.Bold(true).Render("DRY-RUN MODE"))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(T.Warn.GetForeground()).
		Padding(0, 2).
		Width(80).
		Render(content)
	fmt.Println(box)
	fmt.Println()
}

func joinSuites(suites []string) string {
	result := ""
	for i, s := range suites {
		if i > 0 {
			result += ", "
		}
		result += s
	}
	return result
}

func fatal(msg string) {
	fmt.Fprintf(os.Stderr, "%s %s\n", T.Error.Render("ERROR:"), msg)
	os.Exit(1)
}
