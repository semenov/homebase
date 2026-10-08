package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/semenov/homebase/internal/proto"
)

// Error is what every command fails with; see proto for the codes.
type Error = proto.Error

const (
	CodeUsage        = proto.CodeUsage
	CodeConfig       = proto.CodeConfig
	CodeStackUnknown = proto.CodeStackUnknown
	CodeDepsMissing  = proto.CodeDepsMissing
	CodePortInUse    = proto.CodePortInUse
	CodeStartFailed  = proto.CodeStartFailed
	CodeNotListening = proto.CodeNotListening
	CodeLaunchd      = proto.CodeLaunchd
	CodeNotFound     = proto.CodeServerNotFound
	CodeInternal     = proto.CodeInternal
)

func errf(code, hint, format string, a ...any) *Error { return proto.Errf(code, hint, format, a...) }

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
		return proto.ExitCode(e.Code)
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
	return proto.ExitCode(e.Code)
}
