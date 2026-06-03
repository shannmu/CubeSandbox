package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func RenderReport(run *BenchmarkRun, cfg *Config, comparison *ComparisonResult) {
	if cfg.JSONOnly {
		return
	}

	renderSummaryPanel(run)

	for _, suiteName := range cfg.Suites {
		sr, ok := run.Suites[suiteName]
		if !ok {
			continue
		}
		var deltas []WorkloadDelta
		if comparison != nil {
			deltas = comparison.Deltas[suiteName]
		}
		renderSuitePanel(sr, deltas)
	}

	if comparison != nil {
		renderComparisonSummary(comparison)
	}
}

func renderSummaryPanel(run *BenchmarkRun) {
	kvs := []kvPair{
		{"Label", valueOrDefault(run.Label, "(none)")},
		{"Total Time", T.Accent.Render(fmt.Sprintf("%.1fs", run.Summary.TotalDuration))},
		{"Suites", T.Accent.Render(fmt.Sprintf("%d", run.Summary.SuitesRun))},
		{"Workloads", T.Accent.Render(fmt.Sprintf("%d", run.Summary.WorkloadsRun))},
		{"Errors", renderErrorCount(run.Summary.ErrorCount)},
	}
	if run.Environment.SandboxCPU > 0 {
		kvs = append(kvs, kvPair{"Sandbox", T.Value.Render(fmt.Sprintf("%d vCPU / %d MB", run.Environment.SandboxCPU, run.Environment.SandboxMem))})
	}

	content := renderKV(kvs)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(T.BorderOK).
		Padding(1, 3).
		Width(80).
		Render(T.Heading.Render("  Summary") + "\n\n" + content)
	fmt.Println()
	fmt.Println(box)
}

func renderSuitePanel(sr SuiteResult, deltas []WorkloadDelta) {
	deltaMap := make(map[string]WorkloadDelta)
	for _, d := range deltas {
		deltaMap[d.Name] = d
	}

	var rows []string
	headerFmt := "  %-20s %10s %8s %6s"
	header := fmt.Sprintf(headerFmt, "WORKLOAD", "VALUE", "UNIT", "CV%")
	if len(deltas) > 0 {
		header = fmt.Sprintf("  %-20s %10s %8s %6s %10s %10s", "WORKLOAD", "VALUE", "UNIT", "CV%", "BASELINE", "OVERHEAD")
	}
	rows = append(rows, T.Muted.Render(header))
	rows = append(rows, T.Muted.Render("  "+strings.Repeat("─", 76)))

	for _, wr := range sr.Workloads {
		valStr := fmt.Sprintf("%10.2f", wr.Stats.Mean)
		cvStr := fmt.Sprintf("%5.1f%%", wr.Stats.CV*100)

		row := fmt.Sprintf("  %-20s %s %8s %6s",
			lipgloss.NewStyle().Bold(true).Render(wr.Name),
			T.Accent.Render(valStr),
			T.Muted.Render(wr.Unit),
			T.Muted.Render(cvStr),
		)

		if d, ok := deltaMap[wr.Name]; ok {
			baseStr := fmt.Sprintf("%10.2f", d.BaselineMean)
			var overheadStr string
			if d.DeltaPct >= 0 {
				overheadStr = fmt.Sprintf("+%.1f%%", d.DeltaPct)
			} else {
				overheadStr = fmt.Sprintf("%.1f%%", d.DeltaPct)
			}
			style := OverheadStyle(d.DeltaPct)
			sig := " "
			if d.Significant {
				sig = "*"
			}
			row = fmt.Sprintf("  %-20s %s %8s %6s %s %s%s",
				lipgloss.NewStyle().Bold(true).Render(wr.Name),
				T.Accent.Render(valStr),
				T.Muted.Render(wr.Unit),
				T.Muted.Render(cvStr),
				T.Muted.Render(baseStr),
				style.Render(fmt.Sprintf("%8s", overheadStr)),
				sig,
			)
		}

		if len(wr.Errors) > 0 {
			row += T.Error.Render(fmt.Sprintf(" [%d err]", len(wr.Errors)))
		}

		rows = append(rows, row)

		if len(wr.Samples) > 1 {
			spark := Sparkline(wr.Samples, 20)
			rows = append(rows, fmt.Sprintf("  %-20s %s  %s",
				"",
				T.Muted.Render(spark),
				T.Muted.Render(fmt.Sprintf("%.1f .. %.1f", Min(wr.Samples), Max(wr.Samples))),
			))
		}
	}

	title := fmt.Sprintf("  %s  (%s)", strings.ToUpper(sr.Name), T.Muted.Render(fmt.Sprintf("%.1fs", sr.Duration)))
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(T.Border).
		Padding(1, 2).
		Width(80).
		Render(T.Heading.Render(title) + "\n\n" + strings.Join(rows, "\n"))
	fmt.Println()
	fmt.Println(box)
}

func renderComparisonSummary(cr *ComparisonResult) {
	grade := cr.OverallGrade
	style := GradeStyle(grade)

	content := fmt.Sprintf(
		"  Baseline: %s    Current: %s\n\n"+
			"  Overall PVM Overhead:  %s\n\n"+
			"  Grade:  %s    %s",
		T.Muted.Render(valueOrDefault(cr.Baseline.Label, "(unlabeled)")),
		T.Accent.Render(valueOrDefault(cr.Current.Label, "(unlabeled)")),
		OverheadStyle(cr.OverallPct).Render(fmt.Sprintf("%.1f%%", cr.OverallPct)),
		style.Reverse(true).Render(fmt.Sprintf(" %s ", grade)),
		T.Muted.Render(gradeDescription(grade)),
	)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(style.GetForeground()).
		Padding(1, 3).
		Width(80).
		Render(T.Heading.Render("  Comparison") + "\n\n" + content)
	fmt.Println()
	fmt.Println(box)
	fmt.Println()
}

func gradeDescription(grade string) string {
	switch grade {
	case "S":
		return "< 2% overhead (near-native)"
	case "A":
		return "2-5% overhead (excellent)"
	case "B":
		return "5-15% overhead (acceptable)"
	case "C":
		return "15-30% overhead (significant)"
	default:
		return "> 30% overhead (concerning)"
	}
}

type kvPair struct {
	Key   string
	Value string
}

func renderKV(pairs []kvPair) string {
	var b strings.Builder
	for _, p := range pairs {
		b.WriteString(fmt.Sprintf("  %-16s %s\n", T.Heading.Render(p.Key), p.Value))
	}
	return b.String()
}

func renderErrorCount(count int) string {
	if count == 0 {
		return T.OK.Render("0")
	}
	return T.Error.Render(fmt.Sprintf("%d", count))
}

func valueOrDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
