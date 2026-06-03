// Adapted from examples/cube-bench/theme.go

package main

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

type Theme struct {
	Banner   lipgloss.Style
	Heading  lipgloss.Style
	Value    lipgloss.Style
	Accent   lipgloss.Style
	Muted    lipgloss.Style
	OK       lipgloss.Style
	Warn     lipgloss.Style
	Error    lipgloss.Style
	Border   lipgloss.Color
	BorderOK lipgloss.Color
}

var T = DarkTheme

var DarkTheme = Theme{
	Banner:   lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
	Heading:  lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true),
	Value:    lipgloss.NewStyle().Foreground(lipgloss.Color("255")),
	Accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("212")),
	Muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
	OK:       lipgloss.NewStyle().Foreground(lipgloss.Color("82")),
	Warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
	Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("196")),
	Border:   lipgloss.Color("240"),
	BorderOK: lipgloss.Color("82"),
}

var LightTheme = Theme{
	Banner:   lipgloss.NewStyle().Foreground(lipgloss.Color("27")),
	Heading:  lipgloss.NewStyle().Foreground(lipgloss.Color("25")).Bold(true),
	Value:    lipgloss.NewStyle().Foreground(lipgloss.Color("0")),
	Accent:   lipgloss.NewStyle().Foreground(lipgloss.Color("128")),
	Muted:    lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
	OK:       lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
	Warn:     lipgloss.NewStyle().Foreground(lipgloss.Color("166")),
	Error:    lipgloss.NewStyle().Foreground(lipgloss.Color("160")),
	Border:   lipgloss.Color("250"),
	BorderOK: lipgloss.Color("28"),
}

func DetectTheme() Theme {
	if os.Getenv("COLORFGBG") != "" {
		return LightTheme
	}
	return DarkTheme
}

func OverheadStyle(pct float64) lipgloss.Style {
	switch {
	case pct < 5:
		return T.OK
	case pct < 20:
		return T.Warn
	default:
		return T.Error
	}
}

func GradeStyle(grade string) lipgloss.Style {
	switch grade {
	case "S":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	case "A":
		return T.OK.Bold(true)
	case "B":
		return T.Warn.Bold(true)
	case "C":
		return T.Warn.Bold(true)
	default:
		return T.Error.Bold(true)
	}
}
