package main

import "math"

type ComparisonResult struct {
	Baseline     *BenchmarkRun
	Current      *BenchmarkRun
	Deltas       map[string][]WorkloadDelta
	OverallPct   float64
	OverallGrade string
}

type WorkloadDelta struct {
	Name           string
	BaselineMean   float64
	CurrentMean    float64
	DeltaPct       float64
	Unit           string
	HigherIsBetter bool
	Significant    bool
}

func ComputeComparison(baseline, current *BenchmarkRun) *ComparisonResult {
	cr := &ComparisonResult{
		Baseline: baseline,
		Current:  current,
		Deltas:   make(map[string][]WorkloadDelta),
	}

	var overheadFactors []float64

	for suiteName, currentSuite := range current.Suites {
		baselineSuite, ok := baseline.Suites[suiteName]
		if !ok {
			continue
		}

		baselineMap := make(map[string]WorkloadResult)
		for _, wr := range baselineSuite.Workloads {
			baselineMap[wr.Name] = wr
		}

		var deltas []WorkloadDelta
		for _, cwr := range currentSuite.Workloads {
			bwr, ok := baselineMap[cwr.Name]
			if !ok || bwr.Stats.Mean == 0 {
				continue
			}

			delta := WorkloadDelta{
				Name:           cwr.Name,
				BaselineMean:   bwr.Stats.Mean,
				CurrentMean:    cwr.Stats.Mean,
				Unit:           cwr.Unit,
				HigherIsBetter: cwr.HigherIsBetter,
			}

			if cwr.HigherIsBetter {
				delta.DeltaPct = (bwr.Stats.Mean - cwr.Stats.Mean) / bwr.Stats.Mean * 100
			} else {
				delta.DeltaPct = (cwr.Stats.Mean - bwr.Stats.Mean) / bwr.Stats.Mean * 100
			}

			maxCV := math.Max(bwr.Stats.CV, cwr.Stats.CV)
			if maxCV > 0 {
				delta.Significant = math.Abs(delta.DeltaPct) > maxCV*200
			} else {
				delta.Significant = math.Abs(delta.DeltaPct) > 1.0
			}

			deltas = append(deltas, delta)

			factor := 1.0 + delta.DeltaPct/100.0
			if factor > 0 {
				overheadFactors = append(overheadFactors, factor)
			}
		}

		cr.Deltas[suiteName] = deltas
	}

	if len(overheadFactors) > 0 {
		gm := GeometricMean(overheadFactors)
		cr.OverallPct = (gm - 1.0) * 100.0
	}

	cr.OverallGrade = gradeOverhead(cr.OverallPct)
	return cr
}

func gradeOverhead(pct float64) string {
	switch {
	case pct < 2:
		return "S"
	case pct < 5:
		return "A"
	case pct < 15:
		return "B"
	case pct < 30:
		return "C"
	default:
		return "D"
	}
}
