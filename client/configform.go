package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const inputWidth = 32

// configForm is the interactive editor behind `beacon config`. It edits the
// file only: folding in an env or flag value would silently freeze something
// the operator meant to stay dynamic.
type configForm struct {
	inputs  []textinput.Model
	current *resolved
	focus   int
	err     string
	done    bool
	saved   bool
}

func newConfigForm(r *resolved) *configForm {
	f := &configForm{current: r}
	for i, key := range configKeys {
		ti := textinput.New()
		ti.Prompt = ""
		ti.Width = inputWidth
		ti.SetValue(r.value(key))
		ti.TextStyle = styValue
		ti.Cursor.Style = styValue
		if i == 0 {
			ti.Focus()
		}
		f.inputs = append(f.inputs, ti)
	}
	return f
}

func (f *configForm) Init() tea.Cmd { return textinput.Blink }

func (f *configForm) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		var cmd tea.Cmd
		f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
		return f, cmd
	}

	switch key.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		f.done = true
		return f, tea.Quit

	case tea.KeyEnter:
		if err := f.validate(); err != nil {
			f.err = err.Error()
			return f, nil
		}
		f.done, f.saved = true, true
		return f, tea.Quit

	case tea.KeyTab, tea.KeyDown:
		f.move(1)
		return f, textinput.Blink

	case tea.KeyShiftTab, tea.KeyUp:
		f.move(-1)
		return f, textinput.Blink
	}

	f.err = ""
	var cmd tea.Cmd
	f.inputs[f.focus], cmd = f.inputs[f.focus].Update(msg)
	return f, cmd
}

func (f *configForm) move(delta int) {
	f.inputs[f.focus].Blur()
	f.focus = (f.focus + delta + len(f.inputs)) % len(f.inputs)
	f.inputs[f.focus].Focus()
}

func (f *configForm) View() string {
	p := &panel{title: "config", right: version}
	p.blank()

	for i, key := range configKeys {
		caret := "  "
		if i == f.focus && !f.done {
			caret = styValue.Render("▸ ")
		}
		value := f.inputs[i].View()
		if f.done {
			value = styValue.Render(fixed(f.inputs[i].Value(), inputWidth))
		}
		p.line(caret + styLabel.Render(fixed(key, labelWidth-1)+" ") + value)
	}

	p.blank()
	if f.err != "" {
		p.line(styErr.Render("! ") + styErr.Render(f.err))
		p.blank()
	} else if !f.done {
		p.line(styLabel.Render(fixed("from", labelWidth-1)+" ") +
			styDim.Render(string(f.current.sources[configKeys[f.focus]])))
		p.blank()
	}

	if f.done {
		p.footer = f.current.path
	} else {
		p.footer = "tab next · enter save · esc cancel"
	}
	return p.render()
}

func (f *configForm) validate() error {
	server := strings.TrimSpace(f.inputs[0].Value())
	id := strings.TrimSpace(f.inputs[1].Value())
	caFile := strings.TrimSpace(f.inputs[2].Value())

	if id == "" {
		return errors.New("id cannot be empty")
	}
	u, err := url.Parse(server)
	if err != nil {
		return fmt.Errorf("server: %v", err)
	}
	if u.Host == "" {
		return errors.New("server: missing host, e.g. https://relay.example.com")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("server: scheme must be http or https")
	}
	// a ca-file that is not there means https fails and the machine goes dark,
	// so it is worth catching now rather than at the next reconnect
	if caFile != "" {
		if _, err := os.Stat(caFile); err != nil {
			return fmt.Errorf("ca-file: %v", err)
		}
	}
	return nil
}

func (f *configForm) result() Config {
	return Config{
		Server:  strings.TrimSpace(f.inputs[0].Value()),
		AgentID: strings.TrimSpace(f.inputs[1].Value()),
		CAFile:  strings.TrimSpace(f.inputs[2].Value()),
	}
}

func runConfigForm(r *resolved) error {
	form := newConfigForm(r)
	if _, err := tea.NewProgram(form).Run(); err != nil {
		return err
	}
	if !form.saved {
		fmt.Println(styDim.Render("  cancelled, nothing written"))
		return nil
	}

	path, err := saveConfig(form.result())
	if err != nil {
		return err
	}
	fmt.Printf("  %s written\n", path)

	if svc, err := serviceStatus(); err == nil && svc.Running {
		fmt.Println(styDim.Render("  restart the agent:  beacon restart"))
	}
	return nil
}

// isInteractive reports whether both ends are a terminal. Piped or redirected,
// `beacon config` must stay the read-only panel it was.
func isInteractive() bool {
	in, err := os.Stdin.Stat()
	if err != nil || in.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	out, err := os.Stdout.Stat()
	return err == nil && out.Mode()&os.ModeCharDevice != 0
}
