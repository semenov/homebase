package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// ---- colour ---------------------------------------------------------------

// palette degrades to plain text when the stream is not a terminal, NO_COLOR
// is set or TERM=dumb, so output piped into a file or an agent stays clean.
type palette struct{ on, truecolor bool }

func newPalette(f *os.File) palette {
	st, _ := f.Stat()
	tty := st != nil && st.Mode()&os.ModeCharDevice != 0
	if !tty || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{}
	}
	ct := os.Getenv("COLORTERM")
	return palette{on: true, truecolor: ct == "truecolor" || ct == "24bit"}
}

func (p palette) rgb(r, g, b int, s string) string {
	if !p.on {
		return s
	}
	if !p.truecolor {
		switch {
		case r > g && r > b:
			return "\033[91m" + s + "\033[0m"
		case g > b:
			return "\033[92m" + s + "\033[0m"
		}
		return "\033[94m" + s + "\033[0m"
	}
	return fmt.Sprintf("\033[38;2;%d;%d;%dm%s\033[0m", r, g, b, s)
}

func (p palette) blue(s string) string   { return p.rgb(99, 140, 255, s) }
func (p palette) violet(s string) string { return p.rgb(190, 110, 255, s) }
func (p palette) green(s string) string  { return p.rgb(74, 222, 128, s) }
func (p palette) red(s string) string    { return p.rgb(255, 110, 110, s) }
func (p palette) amber(s string) string  { return p.rgb(253, 200, 90, s) }
func (p palette) dim(s string) string {
	if !p.on {
		return s
	}
	return "\033[2m" + s + "\033[0m"
}
func (p palette) bold(s string) string {
	if !p.on {
		return s
	}
	return "\033[1m" + s + "\033[0m"
}

// fade colours a string left to right, indigo into violet, like the logo.
func (p palette) fade(s string) string {
	if !p.on {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		t := 0.0
		if len(runes) > 1 {
			t = float64(i) / float64(len(runes)-1)
		}
		b.WriteString(p.rgb(int(99+(190-99)*t), int(140+(110-140)*t), 255, string(r)))
	}
	return b.String()
}

func termWidth() int {
	var ws struct{ rows, cols, x, y uint16 }
	_, _, err := syscall.Syscall(syscall.SYS_IOCTL, os.Stdout.Fd(),
		uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&ws)))
	if err != 0 || ws.cols == 0 {
		return 80
	}
	return int(ws.cols)
}

// ---- results (stdout) -----------------------------------------------------

// UI prints human-readable results to stdout so that every command looks like
// one program. With --json nothing goes through it; see emit.
type UI struct {
	p     palette
	w     io.Writer
	blank bool // last output ended with an empty line
}

func newUI() *UI { return &UI{p: newPalette(os.Stdout), w: os.Stdout} }

func (u *UI) printf(format string, a ...any) { fmt.Fprintf(u.w, format, a...) }

func (u *UI) gap() {
	if !u.blank {
		u.printf("\n")
		u.blank = true
	}
}

// OK, Off and Bad are status headlines: running, stopped, broken.
func (u *UI) OK(format string, a ...any) { u.headline(u.p.green("●"), format, a...) }
func (u *UI) Off(format string, a ...any) {
	u.headline(u.p.dim("○"), format, a...)
}
func (u *UI) Bad(format string, a ...any)  { u.headline(u.p.red("✗"), format, a...) }
func (u *UI) Warn(format string, a ...any) { u.headline(u.p.amber("!"), format, a...) }

func (u *UI) headline(mark, format string, a ...any) {
	u.gap()
	u.printf("  %s %s\n\n", mark, u.p.bold(fmt.Sprintf(format, a...)))
	u.blank = true
}

// Head labels a block.
func (u *UI) Head(title string) {
	u.gap()
	u.printf("  %s\n\n", u.p.violet(title))
	u.blank = true
}

const keyWidth = 10

// KV prints an aligned label and value. Values are blue: the part worth copying.
func (u *UI) KV(key, value string) {
	pad := keyWidth - len(key)
	if pad < 1 {
		pad = 1
	}
	u.printf("    %s%s%s\n", u.p.dim(key), strings.Repeat(" ", pad), u.p.blue(value))
	u.blank = false
}

// Para prints a short explanation, wrapped and dimmed.
func (u *UI) Para(format string, a ...any) {
	u.printf("%s", wrapDim(u.p, fmt.Sprintf(format, a...), "    "))
	u.printf("\n")
	u.blank = true
}

// Hint suggests the next command.
func (u *UI) Hint(text, cmd string) {
	u.gap()
	u.printf("    %s %s\n\n", u.p.dim(text), u.p.blue(cmd))
	u.blank = true
}

func (u *UI) Line(s string) {
	u.printf("%s\n", s)
	u.blank = false
}

func (u *UI) End() { u.gap() }

func wrapDim(p palette, text, indent string) string {
	width := termWidth() - len(indent) - 2
	if width > 72 {
		width = 72
	}
	if width < 32 {
		width = 32
	}
	var b strings.Builder
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len(line)+1+len(w) > width {
			b.WriteString(indent + p.dim(line) + "\n")
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		b.WriteString(indent + p.dim(line) + "\n")
	}
	return b.String()
}

// ---- progress (stderr) ----------------------------------------------------

var errPalette = newPalette(os.Stderr)

// step reports progress on stderr, so stdout stays a clean result.
func step(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "  %s %s\n", errPalette.blue("→"), fmt.Sprintf(format, a...))
}

// Spinner shows that something is happening while we wait. It is silent
// unless stderr is a terminal.
type Spinner struct {
	stop chan struct{}
	wg   sync.WaitGroup
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func spin(label string) *Spinner {
	s := &Spinner{stop: make(chan struct{})}
	if !errPalette.on {
		return s
	}
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		start := time.Now()
		for i := 0; ; i++ {
			select {
			case <-s.stop:
				fmt.Fprint(os.Stderr, "\r\033[K")
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r\033[K  %s %s %s", errPalette.blue(spinFrames[i%len(spinFrames)]),
					label, errPalette.dim(fmt.Sprintf("%ds", int(time.Since(start).Seconds()))))
			}
		}
	}()
	return s
}

func (s *Spinner) Stop() {
	select {
	case <-s.stop:
	default:
		close(s.stop)
	}
	s.wg.Wait()
}

func short(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}
