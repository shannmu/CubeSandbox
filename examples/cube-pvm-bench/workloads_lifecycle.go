package main

func lifecycleWorkloads() []Workload {
	return []Workload{
		{
			Name:           "create",
			Suite:          "lifecycle",
			Description:    "Sandbox creation latency",
			Unit:           "ms",
			HigherIsBetter: false,
			Command:        "__lifecycle_create__",
			ParseResult:    parseFloat,
		},
		{
			Name:           "snapshot",
			Suite:          "lifecycle",
			Description:    "Sandbox snapshot latency",
			Unit:           "ms",
			HigherIsBetter: false,
			Command:        "__lifecycle_snapshot__",
			ParseResult:    parseFloat,
		},
		{
			Name:           "clone",
			Suite:          "lifecycle",
			Description:    "Sandbox clone (snapshot + create-from-snapshot)",
			Unit:           "ms",
			HigherIsBetter: false,
			Command:        "__lifecycle_clone__",
			ParseResult:    parseFloat,
		},
	}
}
