package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Colours are adaptive so the panel reads on both light and dark terminals.
// lipgloss drops them entirely when stdout is not a terminal, which keeps
// piped output clean without any check of our own.
var (
	cBorder = lipgloss.AdaptiveColor{Light: "#B4BCC6", Dark: "#3F4854"}
	cLabel  = lipgloss.AdaptiveColor{Light: "#6B7280", Dark: "#8B96A3"}
	cValue  = lipgloss.AdaptiveColor{Light: "#111827", Dark: "#D7DEE7"}
	cDim    = lipgloss.AdaptiveColor{Light: "#9CA3AF", Dark: "#6B7280"}
	cOK     = lipgloss.AdaptiveColor{Light: "#15803D", Dark: "#3FB950"}
	cWarn   = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#D29922"}
	cErr    = lipgloss.AdaptiveColor{Light: "#B91C1C", Dark: "#F85149"}

	styBorder = lipgloss.NewStyle().Foreground(cBorder)
	styLabel  = lipgloss.NewStyle().Foreground(cLabel)
	styValue  = lipgloss.NewStyle().Foreground(cValue)
	styTitle  = lipgloss.NewStyle().Foreground(cValue).Bold(true)
	styDim    = lipgloss.NewStyle().Foreground(cDim)
	styOK     = lipgloss.NewStyle().Foreground(cOK).Bold(true)
	styWarn   = lipgloss.NewStyle().Foreground(cWarn).Bold(true)
	styErr    = lipgloss.NewStyle().Foreground(cErr).Bold(true)
)

const (
	labelWidth = 10
	minInner   = 44
	// keep the panel inside an 80 column terminal; anything longer is elided
	maxInner = 72
	sidePad  = 2
)

// panel draws a rounded box with the title written into the top border and a
// note into the bottom one. lipgloss has no API for that, so the border is
// assembled here; lipgloss still supplies colour handling and ANSI-aware width.
type panel struct {
	title  string
	right  string
	footer string
	rows   []string
}

func (p *panel) blank()        { p.rows = append(p.rows, "") }
func (p *panel) line(s string) { p.rows = append(p.rows, s) }

func (p *panel) kv(label, value string) {
	value = elide(value, maxInner-labelWidth)
	p.rows = append(p.rows, styLabel.Render(label2col(label))+styValue.Render(value))
}

// kv2 puts two label/value pairs on one row, which keeps the panel compact
// without making it wide.
func (p *panel) kv2(l1, v1, l2, v2 string) {
	left := styLabel.Render(label2col(l1)) + styValue.Render(fixed(v1, 14))
	p.rows = append(p.rows, left+styLabel.Render(fixed(l2, 9))+styValue.Render(v2))
}

// Marks carry the meaning colour alone cannot: a tick for something that was
// done, a filled dot for something that is live, a hollow one for idle.
const (
	markDone = "✓"
	markLive = "●"
	markIdle = "○"
	markFail = "✗"
	markWarn = "!"
)

func (p *panel) statusMark(style lipgloss.Style, mark, state, detail string) {
	row := style.Render(mark + " " + state)
	if detail != "" {
		row += strings.Repeat(" ", max(1, 21-lipgloss.Width(row))) + styDim.Render(detail)
	}
	p.rows = append(p.rows, row)
}

func (p *panel) status(style lipgloss.Style, state, detail string) {
	p.statusMark(style, markLive, state, detail)
}

// resultPanel is the shape every command reports through, so the answer to
// "did that work" always looks the same and is never absent.
func resultPanel(title, mark string, style lipgloss.Style, state, detail string) *panel {
	p := &panel{title: title, right: version}
	p.blank()
	p.statusMark(style, mark, state, detail)
	p.blank()
	return p
}

// show prints a panel, adding the trailing blank line the layout expects.
func (p *panel) show() {
	p.blank()
	fmt.Print(p.render())
}

// step reports progress for work slow enough that silence would look like a hang.
func step(format string, args ...any) {
	fmt.Println(styDim.Render("  " + fmt.Sprintf(format, args...)))
}

func errorPanel(command string, err error) string {
	p := resultPanel(command, markFail, styErr, "FAILED", "")
	p.kv("error", err.Error())
	p.blank()
	return p.render()
}

func (p *panel) render() string {
	inner := minInner
	for _, r := range p.rows {
		inner = max(inner, lipgloss.Width(r))
	}
	p.footer = elide(p.footer, maxInner)
	inner = max(inner, lipgloss.Width(p.title)+lipgloss.Width(p.right)+6-2*sidePad)
	inner = max(inner, lipgloss.Width(p.footer)+3-2*sidePad)
	inner = min(inner, maxInner)

	var b strings.Builder
	b.WriteString(edge("╭", "╮", p.title, p.right, inner, styTitle))
	for _, r := range p.rows {
		gap := inner - lipgloss.Width(r)
		b.WriteString(styBorder.Render("│") + strings.Repeat(" ", sidePad) +
			r + strings.Repeat(" ", gap+sidePad) + styBorder.Render("│") + "\n")
	}
	b.WriteString(edge("╰", "╯", p.footer, "", inner, styDim))
	return b.String()
}

// edge builds one horizontal border, optionally with text let into it on the
// left and on the right.
func edge(open, close, left, right string, inner int, leftStyle lipgloss.Style) string {
	total := inner + 2*sidePad
	used := 0

	var b strings.Builder
	b.WriteString(styBorder.Render(open))
	if left != "" {
		b.WriteString(styBorder.Render("─ "))
		b.WriteString(leftStyle.Render(left))
		b.WriteString(" ")
		used += 3 + lipgloss.Width(left)
	}
	if right != "" {
		used += 3 + lipgloss.Width(right)
	}
	b.WriteString(styBorder.Render(strings.Repeat("─", max(1, total-used))))
	if right != "" {
		b.WriteString(" " + styDim.Render(right) + styBorder.Render(" ─"))
	}
	b.WriteString(styBorder.Render(close) + "\n")
	return b.String()
}

// label2col pads a label to the value column, always leaving at least one
// space so a label exactly as wide as the column does not touch its value.
func label2col(label string) string {
	return fixed(label, labelWidth-1) + " "
}

// fixed pads to a display width, leaving already-styled text alone.
func fixed(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// elide shortens plain text to fit. A path loses its head, because the file
// name is what identifies it; anything else loses its tail, because the first
// words are what explain it.
//
// The test is deliberately narrow. Asking merely whether a slash appears makes
// "i/o timeout" look like a path, and an error message elided from the wrong
// end says nothing at all.
func elide(s string, w int) string {
	if w < 4 || lipgloss.Width(s) <= w {
		return s
	}
	r := []rune(s)
	if looksLikePath(s) {
		return "…" + string(r[len(r)-(w-1):])
	}
	return string(r[:w-1]) + "…"
}

func looksLikePath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) {
		return true
	}
	// a windows path: a drive letter, a colon, a separator
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') {
		c := s[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	return false
}

func humanBytes(n uint64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	v, exp := float64(n)/unit, 0
	for v >= unit && exp < 3 {
		v /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", v, "KMGT"[exp])
}
