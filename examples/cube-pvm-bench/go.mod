module github.com/tencentcloud/CubeSandbox/examples/cube-pvm-bench

go 1.22.0

require (
	github.com/charmbracelet/bubbles v1.0.0
	github.com/charmbracelet/bubbletea v1.3.10
	github.com/charmbracelet/lipgloss v1.1.0
	github.com/tencentcloud/CubeSandbox/sdk/go v0.0.0
	golang.org/x/term v0.41.0
)

replace github.com/tencentcloud/CubeSandbox/sdk/go => ../../sdk/go
