package main

import (
	"fmt"
	"strconv"
	"strings"
)

type Workload struct {
	Name           string
	Suite          string
	Description    string
	Command        string
	ParseResult    func(stdout, stderr string) (float64, error)
	Unit           string
	HigherIsBetter bool
}

type Suite struct {
	Name      string
	Workloads []Workload
}

var registry = map[string]Suite{
	"cpu":       {Name: "cpu", Workloads: cpuWorkloads()},
	"memory":    {Name: "memory", Workloads: memoryWorkloads()},
	"disk":      {Name: "disk", Workloads: diskWorkloads()},
	"network":   {Name: "network", Workloads: networkWorkloads()},
	"syscall":   {Name: "syscall", Workloads: syscallWorkloads()},
	"lifecycle": {Name: "lifecycle", Workloads: lifecycleWorkloads()},
}

func getSuites(names []string) ([]Suite, error) {
	var suites []Suite
	for _, name := range names {
		s, ok := registry[name]
		if !ok {
			return nil, fmt.Errorf("unknown suite: %q (available: %s)", name, strings.Join(allSuites, ", "))
		}
		suites = append(suites, s)
	}
	return suites, nil
}

func parseFloat(stdout, _ string) (float64, error) {
	s := strings.TrimSpace(stdout)
	lines := strings.Split(s, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	v, err := strconv.ParseFloat(last, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", last, err)
	}
	return v, nil
}
