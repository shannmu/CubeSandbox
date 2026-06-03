package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tickMsg time.Time
type progressMsg ProgressEvent
type doneMsg struct{}

type model struct {
	cfg        *Config
	progressCh <-chan ProgressEvent
	progress   progress.Model
	events     []ProgressEvent
	current    ProgressEvent
	total      int
	completed  int
	startTime  time.Time
	done       bool
	width      int
}

func newUIModel(cfg *Config, totalWorkloads int, progressCh <-chan ProgressEvent) model {
	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(50),
	)
	return model{
		cfg:        cfg,
		progressCh: progressCh,
		progress:   p,
		total:      totalWorkloads,
		startTime:  time.Now(),
		width:      80,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(tickCmd(), waitForProgress(m.progressCh))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			return m, tea.Quit
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.progress.Width = min(msg.Width-10, 60)
	case tickMsg:
		return m, tickCmd()
	case progressMsg:
		ev := ProgressEvent(msg)
		if ev.Done {
			m.done = true
			return m, tea.Quit
		}
		if ev.Err != "" {
			m.events = append(m.events, ev)
			return m, waitForProgress(m.progressCh)
		}
		m.current = ev
		if ev.Iter == ev.Total {
			m.completed++
		}
		m.events = append(m.events, ev)
		return m, waitForProgress(m.progressCh)
	case progress.FrameMsg:
		progressModel, cmd := m.progress.Update(msg)
		m.progress = progressModel.(progress.Model)
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	if m.done {
		return ""
	}

	var b strings.Builder

	pct := 0.0
	if m.total > 0 {
		pct = float64(m.completed) / float64(m.total)
	}

	elapsed := time.Since(m.startTime)

	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("  %s  %s  %s\n\n",
		T.Heading.Render("Running benchmarks..."),
		T.Muted.Render(fmt.Sprintf("[%d/%d]", m.completed, m.total)),
		T.Muted.Render(fmt.Sprintf("%.0fs", elapsed.Seconds())),
	))

	b.WriteString(fmt.Sprintf("  %s\n\n", m.progress.ViewAs(pct)))

	if m.current.Workload != "" {
		b.WriteString(fmt.Sprintf("  %s %s  iter %d/%d",
			T.Accent.Render(m.current.Suite+"/"),
			lipgloss.NewStyle().Bold(true).Render(m.current.Workload),
			m.current.Iter,
			m.current.Total,
		))
		if m.current.Value > 0 {
			b.WriteString(fmt.Sprintf("  = %s %s",
				T.OK.Render(fmt.Sprintf("%.2f", m.current.Value)),
				T.Muted.Render(m.current.Unit),
			))
		}
		b.WriteString("\n")
	}

	recentCount := 5
	if len(m.events) < recentCount {
		recentCount = len(m.events)
	}
	if recentCount > 0 {
		b.WriteString("\n")
		start := len(m.events) - recentCount
		for _, ev := range m.events[start:] {
			if ev.Err != "" {
				b.WriteString(fmt.Sprintf("  %s %s\n",
					T.Error.Render("ERR"),
					T.Muted.Render(ev.Err),
				))
			} else {
				b.WriteString(fmt.Sprintf("  %s %-18s = %s %s\n",
					T.Muted.Render("OK "),
					ev.Suite+"/"+ev.Workload,
					T.Value.Render(fmt.Sprintf("%.2f", ev.Value)),
					T.Muted.Render(ev.Unit),
				))
			}
		}
	}

	b.WriteString("\n")
	return b.String()
}

func tickCmd() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func waitForProgress(ch <-chan ProgressEvent) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-ch
		if !ok {
			return progressMsg(ProgressEvent{Done: true})
		}
		return progressMsg(ev)
	}
}

func collectSimpleProgress(ch <-chan ProgressEvent, cfg *Config) {
	lastSuite := ""
	for ev := range ch {
		if ev.Done {
			break
		}
		if ev.Err != "" {
			fmt.Printf("  %s %s\n", T.Error.Render("ERR"), ev.Err)
			continue
		}
		if ev.Suite != lastSuite {
			if lastSuite != "" {
				fmt.Println()
			}
			fmt.Printf("  %s\n", T.Heading.Render(strings.ToUpper(ev.Suite)))
			lastSuite = ev.Suite
		}
		fmt.Printf("    %-18s [%d/%d] = %s %s\n",
			ev.Workload,
			ev.Iter, ev.Total,
			T.Accent.Render(fmt.Sprintf("%.2f", ev.Value)),
			T.Muted.Render(ev.Unit),
		)
	}
	fmt.Println()
}
