package main

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strings"
	"time"
)

type Collector interface {
	Name() string
	Unit() string
	Init(pids []string)
	Collect(pids []string) float64
}

type ObservationSample struct {
	T       float64            `json:"t"`
	Metrics map[string]float64 `json:"metrics"`
}

type Observations struct {
	SandboxID string                  `json:"sandbox_id"`
	Metrics   []MetricMeta            `json:"metrics"`
	Samples   []ObservationSample     `json:"samples"`
	Summary   map[string]MetricSummary `json:"summary"`
}

type MetricMeta struct {
	Name string `json:"name"`
	Unit string `json:"unit"`
}

type MetricSummary struct {
	Min float64 `json:"min"`
	Max float64 `json:"max"`
	Avg float64 `json:"avg"`
}

func DefaultCollectors() []Collector {
	return []Collector{
		&RSSCollector{},
		&CPUCollector{},
	}
}

func StartObserver(ctx context.Context, sandboxID string, interval time.Duration, collectors []Collector) (stop func(), result <-chan *Observations) {
	ch := make(chan *Observations, 1)
	obsCtx, cancel := context.WithCancel(ctx)

	go func() {
		obs := &Observations{
			SandboxID: sandboxID,
			Metrics:   make([]MetricMeta, 0, len(collectors)),
		}
		for _, c := range collectors {
			obs.Metrics = append(obs.Metrics, MetricMeta{Name: c.Name(), Unit: c.Unit()})
		}

		pids := findPIDs(sandboxID)
		for _, c := range collectors {
			c.Init(pids)
		}

		start := time.Now()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-obsCtx.Done():
				obs.Summary = computeObsSummary(obs.Samples, collectors)
				ch <- obs
				return
			case <-ticker.C:
				sample := ObservationSample{
					T:       math.Round(time.Since(start).Seconds()*10) / 10,
					Metrics: make(map[string]float64, len(collectors)),
				}
				for _, c := range collectors {
					sample.Metrics[c.Name()] = math.Round(c.Collect(pids)*10) / 10
				}
				obs.Samples = append(obs.Samples, sample)
			}
		}
	}()

	return cancel, ch
}

func findPIDs(sandboxID string) []string {
	out, err := exec.Command("pgrep", "-f", sandboxID).Output()
	if err != nil {
		return nil
	}
	var pids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			pids = append(pids, line)
		}
	}
	return pids
}

func computeObsSummary(samples []ObservationSample, collectors []Collector) map[string]MetricSummary {
	summary := make(map[string]MetricSummary, len(collectors))
	if len(samples) == 0 {
		return summary
	}

	for _, c := range collectors {
		name := c.Name()
		ms := MetricSummary{Min: math.MaxFloat64}
		var sum float64
		var count int
		for _, s := range samples {
			v, ok := s.Metrics[name]
			if !ok {
				continue
			}
			if v < ms.Min {
				ms.Min = v
			}
			if v > ms.Max {
				ms.Max = v
			}
			sum += v
			count++
		}
		if count > 0 {
			ms.Avg = math.Round(sum/float64(count)*10) / 10
		}
		if ms.Min == math.MaxFloat64 {
			ms.Min = 0
		}
		summary[name] = ms
	}
	return summary
}

func formatObservations(obs *Observations) string {
	if obs == nil || len(obs.Samples) == 0 {
		return ""
	}
	var parts []string
	for _, m := range obs.Metrics {
		s, ok := obs.Summary[m.Name]
		if !ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %.1f-%.1f %s (avg %.1f)", m.Name, s.Min, s.Max, m.Unit, s.Avg))
	}
	return strings.Join(parts, ", ")
}
