package protocol

import (
	"fmt"
	"strings"
	"time"
)

// StreamLog carries an agent's own warnings to the relay, as newline-delimited
// JSON, for the lifetime of a session.
//
// It exists because an agent's log is otherwise unreachable: on windows a
// service has no stdout at all, and even where there is a file, reading it
// means already having the access the tunnel was supposed to provide. A refused
// connection has to be able to explain itself where the operator is looking.
const StreamLog = "log"

// LogLine is one record. Only warnings and errors travel: the relay already
// records the ordinary comings and goings, and repeating them here would bury
// the one line that matters.
type LogLine struct {
	Time  time.Time `json:"time"`
	Level string    `json:"level"`
	Msg   string    `json:"msg"`
	Attrs string    `json:"attrs,omitempty"`
}

// Text renders the line the way the dashboard shows it.
func (l LogLine) Text() string {
	if l.Attrs == "" {
		return l.Msg
	}
	return l.Msg + " (" + l.Attrs + ")"
}

// FormatAttr renders one key/value pair for the Attrs field.
func FormatAttr(key string, value any) string {
	return fmt.Sprintf("%s=%v", key, value)
}

// JoinAttrs assembles the rendered pairs.
func JoinAttrs(parts []string) string { return strings.Join(parts, " ") }
