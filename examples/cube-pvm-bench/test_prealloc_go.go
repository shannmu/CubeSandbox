// Copyright (c) 2026 Tencent Inc.
// SPDX-License-Identifier: Apache-2.0

//go:build ignore
// +build ignore

// Standalone prefault verification using the Go SDK.
//
// Usage:
//
//	export CUBE_TEMPLATE_ID=tpl-xxxxx
//	go run test_prealloc_go_test.go
//
// Must run on the ECS host where sandboxes are scheduled so that
// /proc/<vmm-pid>/status is readable.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	cubesandbox "github.com/tencentcloud/CubeSandbox/sdk/go"
)

const (
	sampleInterval = 1 * time.Second
	sampleCount    = 15
)

func main() {
	templateID := os.Getenv("CUBE_TEMPLATE_ID")
	if templateID == "" {
		fatal("CUBE_TEMPLATE_ID environment variable is required")
	}

	ctx := context.Background()
	client := cubesandbox.NewClient(cubesandbox.NewConfigFromEnv())
	defer client.Close()

	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("  CubeSandbox Prefault Verification (Go SDK)")
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println()

	// ── Case 1: prefault=false (default, lazy-load) ──
	fmt.Println("Case 1: Sandbox WITHOUT prefault (lazy-load default)")
	fmt.Println(strings.Repeat("-", 60))

	sbLazy, err := client.Create(ctx, cubesandbox.CreateOptions{
		TemplateID: templateID,
		Prefault:   false,
	})
	if err != nil {
		fatal("Failed to create lazy sandbox: %v", err)
	}
	fmt.Printf("  sandbox: %s\n", sbLazy.SandboxID)

	samplesLazy := casePrefault(ctx, sbLazy)
	printSamples("Lazy-load RSS", samplesLazy)
	sbLazy.Kill(ctx)

	// ── Case 2: prefault=true ──
	fmt.Println()
	fmt.Println("Case 2: Sandbox WITH prefault=true")
	fmt.Println(strings.Repeat("-", 60))

	sbPrefault, err := client.Create(ctx, cubesandbox.CreateOptions{
		TemplateID: templateID,
		Prefault:   true,
	})
	if err != nil {
		fatal("Failed to create prefault sandbox: %v", err)
	}
	fmt.Printf("  sandbox: %s\n", sbPrefault.SandboxID)

	samplesPrefault := casePrefault(ctx, sbPrefault)
	printSamples("Prefault RSS", samplesPrefault)
	sbPrefault.Kill(ctx)

	// ── Summary ──
	fmt.Println()
	fmt.Println(strings.Repeat("=", 60))
	fmt.Println("SUMMARY")
	fmt.Println(strings.Repeat("=", 60))

	sumLazy := summarize("Lazy-load", samplesLazy)
	sumPrefault := summarize("Prefault", samplesPrefault)

	printSummary(sumLazy)
	printSummary(sumPrefault)

	if sumLazy.count > 0 && sumPrefault.count > 0 && sumLazy.mean > 0 {
		ratio := sumPrefault.mean / sumLazy.mean
		fmt.Printf("\n  Prefault / Lazy ratio: %.1fx\n", ratio)
		if ratio > 2 {
			fmt.Println("  >>> Prefault is working: RSS significantly higher upfront")
		} else if ratio > 1.2 {
			fmt.Println("  >> Prefault may be working (moderate difference)")
		} else {
			fmt.Println("  >> RSS difference is small — prefault may not be active")
		}
	}
}

// casePrefault keeps the sandbox alive and samples its VMM RSS.
func casePrefault(ctx context.Context, sb *cubesandbox.Sandbox) []rssSample {
	// Keep sandbox alive with a sleep task in background
	go func() {
		_, _ = sb.RunCode(ctx, "import time; time.sleep(60)", cubesandbox.RunCodeOptions{
			Timeout: 90 * time.Second,
		})
	}()

	// Wait for sandbox to fully start
	time.Sleep(2 * time.Second)

	return sampleRSS(sb.SandboxID, sampleCount)
}

type rssSample struct {
	t     float64
	rssMB *float64
}

// sampleRSS collects VMM RSS samples for a given sandbox.
func sampleRSS(sandboxID string, count int) []rssSample {
	samples := make([]rssSample, 0, count)
	start := time.Now()

	for i := 0; i < count; i++ {
		pids := getSandboxPIDs(sandboxID)
		rssKB := getRSSKB(pids)

		var rssMB *float64
		if rssKB > 0 {
			v := float64(rssKB) / 1024.0
			rssMB = &v
		}

		samples = append(samples, rssSample{
			t:     time.Since(start).Seconds(),
			rssMB: rssMB,
		})
		time.Sleep(sampleInterval)
	}
	return samples
}

// getSandboxPIDs finds VMM process PIDs for a sandbox.
func getSandboxPIDs(sandboxID string) []string {
	out, err := exec.Command("pgrep", "-f", sandboxID).Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pids []string
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			pids = append(pids, l)
		}
	}
	return pids
}

// getRSSKB reads total VmRSS from /proc/<pid>/status for given PIDs.
func getRSSKB(pids []string) int64 {
	var total int64
	for _, pid := range pids {
		data, err := os.ReadFile(fmt.Sprintf("/proc/%s/status", pid))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						total += v
					}
				}
				break
			}
		}
	}
	return total
}

func printSamples(label string, samples []rssSample) {
	fmt.Printf("\n  %s:\n", label)
	fmt.Printf("  %6s  %10s\n", "t", "RSS")
	fmt.Printf("  %6s  %10s\n", "---", "---")
	for _, s := range samples {
		rss := "N/A"
		if s.rssMB != nil {
			rss = fmt.Sprintf("%.1f MB", *s.rssMB)
		}
		fmt.Printf("  %5.1fs  %10s\n", s.t, rss)
	}
}

type summary struct {
	label string
	count int
	min   float64
	max   float64
	mean  float64
}

func summarize(label string, samples []rssSample) summary {
	s := summary{label: label}
	for _, sample := range samples {
		if sample.rssMB != nil {
			if s.count == 0 {
				s.min = *sample.rssMB
				s.max = *sample.rssMB
			}
			s.count++
			if *sample.rssMB < s.min {
				s.min = *sample.rssMB
			}
			if *sample.rssMB > s.max {
				s.max = *sample.rssMB
			}
			s.mean += *sample.rssMB
		}
	}
	if s.count > 0 {
		s.mean /= float64(s.count)
	}
	return s
}

func printSummary(s summary) {
	if s.count == 0 {
		fmt.Printf("  %s: no samples collected\n", s.label)
		return
	}
	fmt.Printf("  %s:\n", s.label)
	fmt.Printf("    RSS  range: %.1f .. %.1f MB\n", s.min, s.max)
	fmt.Printf("    RSS  mean:  %.1f MB\n", s.mean)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}
