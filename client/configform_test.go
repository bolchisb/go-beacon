package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func testForm() *configForm {
	return newConfigForm(&resolved{
		Config:  Config{Server: "http://127.0.0.1:8080", AgentID: "build-vm-01.example.com"},
		sources: map[string]source{keyServer: fromFile, keyID: fromFile, keyCA: fromDefault},
		path:    "/etc/beacon/config.json",
		exists:  true,
	})
}

func send(f *configForm, msgs ...tea.Msg) *configForm {
	var m tea.Model = f
	for _, msg := range msgs {
		m, _ = m.Update(msg)
	}
	return m.(*configForm)
}

func typeRunes(s string) []tea.Msg {
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	return msgs
}

func TestEnterSavesCurrentValues(t *testing.T) {
	f := send(testForm(), tea.KeyMsg{Type: tea.KeyEnter})

	if !f.saved || !f.done {
		t.Fatalf("enter should save and finish, got saved=%v done=%v err=%q", f.saved, f.done, f.err)
	}
	got := f.result()
	if got.Server != "http://127.0.0.1:8080" || got.AgentID != "build-vm-01.example.com" {
		t.Fatalf("unexpected result: %+v", got)
	}
}

func TestEscCancelsWithoutSaving(t *testing.T) {
	f := send(testForm(), tea.KeyMsg{Type: tea.KeyEsc})

	if f.saved {
		t.Fatal("esc must not save")
	}
	if !f.done {
		t.Fatal("esc must finish the form")
	}
}

func TestTabMovesFocusAndTypingEditsTheFocusedField(t *testing.T) {
	msgs := append([]tea.Msg{tea.KeyMsg{Type: tea.KeyTab}}, typeRunes("-x")...)
	f := send(testForm(), msgs...)

	if f.focus != 1 {
		t.Fatalf("tab should move focus to id, got %d", f.focus)
	}
	if got := f.result().AgentID; got != "build-vm-01.example.com-x" {
		t.Fatalf("typing edited the wrong field: id=%q", got)
	}
	if got := f.result().Server; got != "http://127.0.0.1:8080" {
		t.Fatalf("server should be untouched, got %q", got)
	}
}

func TestShiftTabWrapsBackwards(t *testing.T) {
	f := send(testForm(), tea.KeyMsg{Type: tea.KeyShiftTab})
	if f.focus != len(configKeys)-1 {
		t.Fatalf("shift-tab from the first field should wrap to the last, got %d", f.focus)
	}
}

func TestInvalidInputBlocksSaving(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*configForm)
		want  string
	}{
		{"server without host", func(f *configForm) { f.inputs[0].SetValue("nonsense") }, "missing host"},
		{"unsupported scheme", func(f *configForm) { f.inputs[0].SetValue("ftp://relay.example.com") }, "http or https"},
		{"empty id", func(f *configForm) { f.inputs[1].SetValue("  ") }, "id cannot be empty"},
		{"ca-file that is not there", func(f *configForm) { f.inputs[2].SetValue("/no/such/ca.pem") }, "ca-file"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := testForm()
			tc.setup(f)
			f = send(f, tea.KeyMsg{Type: tea.KeyEnter})

			if f.saved {
				t.Fatal("invalid input must not be saved")
			}
			if f.err == "" {
				t.Fatal("expected an error to be shown in the panel")
			}
			if !strings.Contains(f.err, tc.want) {
				t.Fatalf("error %q should mention %q", f.err, tc.want)
			}
		})
	}
}

func TestExistingCAFileIsAccepted(t *testing.T) {
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := testForm()
	f.inputs[2].SetValue(ca)
	f = send(f, tea.KeyMsg{Type: tea.KeyEnter})

	if !f.saved {
		t.Fatalf("a readable ca-file should be accepted, got err=%q", f.err)
	}
}
