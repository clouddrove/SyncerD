// Package logging configures the process logger. Text is the default and
// reproduces the historical log.Printf format exactly, because operators
// grep it. JSON is opt in.
package logging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
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
// level and no key prefix on the message itself. Any attributes are
// appended after the message as " key=value" pairs, which is additive and
// only appears on lines that carry attributes, so a line with none of
// today's calls attach is unchanged.
type textHandler struct {
	w      io.Writer
	mu     *sync.Mutex
	attrs  []slog.Attr
	prefix string // group prefix applied to every key, including future WithAttrs calls
}

func newTextHandler(w io.Writer) *textHandler {
	return &textHandler{w: w, mu: &sync.Mutex{}}
}

func (h *textHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *textHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Time.Local().Format("2006/01/02 15:04:05"))
	b.WriteByte(' ')
	b.WriteString(r.Message)

	for _, a := range h.attrs {
		writeAttr(&b, h.prefix, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		writeAttr(&b, h.prefix, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.w, b.String())
	return err
}

func (h *textHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}
	nh := *h
	nh.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &nh
}

func (h *textHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	nh := *h
	nh.prefix = h.prefix + name + "."
	return &nh
}

// writeAttr appends " key=value" to b, quoting the value only when it
// contains a space.
func writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	key := a.Key
	if prefix != "" {
		key = prefix + key
	}
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(formatValue(a.Value))
}

func formatValue(v slog.Value) string {
	s := v.String()
	if strings.Contains(s, " ") {
		return strconv.Quote(s)
	}
	return s
}
