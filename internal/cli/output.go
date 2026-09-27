package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vsemenov/ship/internal/proto"
)

var jsonOut bool

// info prints a progress line to stderr. stdout is reserved for results so
// `ship --json` output can always be parsed.
func info(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
}

func step(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "→ "+format+"\n", args...)
}

// emit prints a successful result: JSON envelope with --json, otherwise human().
func emit(data any, human func()) {
	if jsonOut {
		b, _ := json.Marshal(data)
		json.NewEncoder(os.Stdout).Encode(proto.Response{OK: true, Data: b})
		return
	}
	human()
}

// fail prints err and returns the process exit code.
func fail(err error) int {
	var pe *proto.Error
	if !errors.As(err, &pe) {
		pe = &proto.Error{Code: proto.CodeInternal, Message: err.Error()}
	}
	if jsonOut {
		json.NewEncoder(os.Stdout).Encode(proto.Response{OK: false, Error: pe})
	} else {
		fmt.Fprintf(os.Stderr, "✗ %s [%s]\n", pe.Message, pe.Code)
		if pe.Hint != "" {
			fmt.Fprintf(os.Stderr, "  hint: %s\n", pe.Hint)
		}
		if pe.Logs != "" {
			fmt.Fprintf(os.Stderr, "  ── logs ──\n  %s\n", strings.ReplaceAll(strings.TrimRight(pe.Logs, "\n"), "\n", "\n  "))
		}
	}
	return proto.ExitCode(pe.Code)
}
