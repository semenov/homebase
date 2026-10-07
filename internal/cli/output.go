package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Error codes are stable: agents may branch on them.
const (
	CodeUsage        = "usage"
	CodeConfig       = "config"
	CodeStackUnknown = "stack_not_detected"
	CodeDepsMissing  = "dependencies_missing"
	CodePortInUse    = "port_in_use"
	CodeStartFailed  = "start_failed"  // the process exited while starting
	CodeNotListening = "not_listening" // running, but the port never opened
	CodeLaunchd      = "launchd_failed"
	CodeCloudflare   = "cloudflare_failed"
	CodeNotFound     = "server_not_found"
	CodeInternal     = "internal"
)

// ExitCode maps error codes to process exit codes.
func ExitCode(code string) int {
	switch code {
	case CodeUsage:
		return 2
	case CodeConfig, CodeStackUnknown, CodeDepsMissing:
		return 3
	case CodePortInUse, CodeStartFailed, CodeNotListening, CodeLaunchd:
		return 5
	case CodeCloudflare:
		return 6
	case CodeNotFound:
		return 7
	}
	return 1
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Logs    string `json:"logs,omitempty"`
}

func (e *Error) Error() string { return e.Message }

func errf(code, hint, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Hint: hint}
}

var jsonOut bool

type response struct {
	OK    bool   `json:"ok"`
	Data  any    `json:"data,omitempty"`
	Error *Error `json:"error,omitempty"`
}

// emit prints a successful result: the JSON envelope with --json, otherwise human().
func emit(data any, human func(u *UI)) {
	if jsonOut {
		writeJSON(response{OK: true, Data: data})
		return
	}
	u := newUI()
	human(u)
	u.End()
}

func writeJSON(v any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}

// fail prints err and returns the process exit code.
func fail(err error) int {
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Code: CodeInternal, Message: err.Error()}
	}
	if jsonOut {
		writeJSON(response{OK: false, Error: e})
		return ExitCode(e.Code)
	}
	p := errPalette
	fmt.Fprintf(os.Stderr, "\n  %s %s  %s\n", p.red("✗"), p.bold(e.Message), p.dim("["+e.Code+"]"))
	if e.Logs != "" {
		fmt.Fprintln(os.Stderr)
		for _, l := range strings.Split(strings.TrimRight(e.Logs, "\n"), "\n") {
			fmt.Fprintf(os.Stderr, "    %s %s\n", p.dim("│"), l)
		}
	}
	if e.Hint != "" {
		fmt.Fprintf(os.Stderr, "\n%s", wrapDim(p, e.Hint, "    "))
	}
	fmt.Fprintln(os.Stderr)
	return ExitCode(e.Code)
}
