package protocol

import "time"

// Operations carried on a StreamRPC stream.
const (
	OpExec      = "exec"
	OpReadFile  = "read_file"
	OpWriteFile = "write_file"
	OpListDir   = "list_dir"
)

// RPCRequest is one call: a single JSON object, answered by a single
// RPCResponse, then the stream closes. One stream per call means yamux already
// provides the concurrency and there is no request id to correlate.
type RPCRequest struct {
	Op string `json:"op"`

	// exec
	Command        string `json:"command,omitempty"`
	Dir            string `json:"dir,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`

	// read_file, write_file, list_dir
	Path string `json:"path,omitempty"`
	// Content is base64 so a write survives arbitrary bytes, not just text.
	Content string `json:"content,omitempty"`
}

// DirEntry is one line of a list_dir result.
type DirEntry struct {
	Name    string    `json:"name"`
	IsDir   bool      `json:"is_dir"`
	Size    int64     `json:"size"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

// RPCResponse answers exactly one RPCRequest. Error carries a failure of the
// call itself; a command that ran and failed reports through ExitCode instead,
// because "the compiler returned 1" is a result, not an error.
type RPCResponse struct {
	Error string `json:"error,omitempty"`

	// exec
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated,omitempty"`

	// read_file
	Content string `json:"content,omitempty"` // base64

	// write_file
	Written int `json:"written,omitempty"`

	// list_dir
	Entries []DirEntry `json:"entries,omitempty"`
}
