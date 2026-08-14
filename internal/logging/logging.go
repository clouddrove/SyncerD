// Package logging configures the process logger. Text is the default and
// reproduces the historical log.Printf format exactly, because operators
// grep it.
//
// Text mode ignores every attribute passed to Info, Warn, or Error: a
// message and its attributes often state the same values, and appending
// them would change a line that operators already grep. Attributes exist
// for JSON consumers only. Do not reintroduce an attribute append to text
// mode; it silently breaks the byte identity guarantee this package
// exists to provide.
//
// JSON is opt in.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
)

// FormatText and FormatJSON are the only values Setup accepts.
const (
	FormatText = "text"
	FormatJSON = "json"
)

var (
	mu     sync.Mutex
	logger = slog.New(newTextHandler(os.Stderr))
)

// Setup configures the package logger. format is "text" or "json"; any
// other value is an error, so a typo fails loudly at startup rather than
// silently logging in the wrong shape.
func Setup(format string, w io.Writer) error {
	var h slog.Handler
	switch format {
	case FormatText:
		h = newTextHandler(w)
	case FormatJSON:
		h = slog.NewJSONHandler(w, nil)
	default:
		return fmt.Errorf("invalid log format %q: want %q or %q", format, FormatText, FormatJSON)
	}

	mu.Lock()
	logger = slog.New(h)
	mu.Unlock()
	return nil
}

// Info logs at info level. Args are slog style alternating keys and values.
func Info(msg string, args ...any) {
	current().Log(context.Background(), slog.LevelInfo, msg, args...)
}

// Warn logs at warn level. Args are slog style alternating keys and values.
func Warn(msg string, args ...any) {
	current().Log(context.Background(), slog.LevelWarn, msg, args...)
}

// Error logs at error level. Args are slog style alternating keys and values.
func Error(msg string, args ...any) {
	current().Log(context.Background(), slog.LevelError, msg, args...)
}

func current() *slog.Logger {
	mu.Lock()
	defer mu.Unlock()
	return logger
}

// textHandler reproduces the historical log.Printf(log.LstdFlags) format:
// "2006/01/02 15:04:05 " followed by the message, in local time, with no
// level and no key prefix on the message itself. Attributes, whether
// passed directly to Info/Warn/Error or attached via WithAttrs, are
// deliberately never written: this handler's only job is to keep matching
// log.Printf forever, and a message that already states its values must
// never have them appended a second time.
type textHandler struct {
	w  io.Writer
	mu *sync.Mutex
}

func newTextHandler(w io.Writer) *textHandler {
	return &textHandler{w: w, mu: &sync.Mutex{}}
}

func (h *textHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	line := r.Time.Local().Format("2006/01/02 15:04:05") + " " + r.Message + "\n"

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, line)
	return err
}

// WithAttrs and WithGroup exist to satisfy slog.Handler. Since Handle never
// writes attributes, there is nothing useful to record here: both return
// the receiver unchanged.
func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler { return h }
func (h *textHandler) WithGroup(name string) slog.Handler       { return h }
