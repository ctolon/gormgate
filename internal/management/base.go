// Package management runs gormgate's management commands.
//
// The command tree is cobra's: Root builds it, each command binds its own
// flags and does its work in RunE, and Context carries what all of them
// need — the project, the streams, the style and the verbosity.
//
// What a command prints is gormgate's own; what it decides is Django's.
// docs/from-django.md maps one onto the other.
package management

import (
	"io"
	"os"
	"strings"

	"github.com/ctolon/gormgate/internal/termcolors"
)

// DefaultAlias is the connection a command works on when --database is
// absent.
const DefaultAlias = "default"

// Env is the process environment of a command run.
type Env struct {
	// The three standard streams.
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	// The isatty tests for each of them, which decide whether output is
	// colored and whether the user can be prompted.
	StdoutIsTTY func() bool
	StderrIsTTY func() bool
	StdinIsTTY  func() bool
	// Getenv looks up an environment variable; nil reads the process
	// environment.
	Getenv func(string) (string, bool)
	// Getwd returns the working directory (paths in messages are relative
	// to it).
	Getwd func() (string, error)
}

func (e *Env) lookup(k string) (string, bool) {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.LookupEnv(k)
}

func (e *Env) termEnv() termcolors.Env {
	return termcolors.Env{Getenv: e.lookup, StdoutIsTTY: e.StdoutIsTTY}
}

// Styler colors a message; the functions of termcolors.Style are Stylers.
type Styler func(string) string

// plain is the Styler that leaves a message as it is, for output that must
// not pick up the stream's default style.
func plain(msg string) string { return msg }

// Stream is a command's stdout or stderr: a message-oriented writer that
// terminates every message with a newline and colors it with the stream's
// style, which is only set when the stream is a terminal.
type Stream struct {
	out   io.Writer
	isTTY func() bool
	style Styler
	err   error
}

// NewStream writes to w, which isTTY reports to be a terminal or not.
func NewStream(w io.Writer, isTTY func() bool) *Stream {
	return &Stream{out: w, isTTY: isTTY}
}

// SetStyle makes style the stream's default; a nil style, or a stream that
// is not a terminal, leaves the messages uncolored.
func (s *Stream) SetStyle(style Styler) {
	if style != nil && s.isTerminal() {
		s.style = style
	} else {
		s.style = nil
	}
}

// isTerminal reports whether the stream is a terminal, which is the only
// case in which its messages are colored.
func (s *Stream) isTerminal() bool { return s.isTTY != nil && s.isTTY() }

// Println writes msg in the stream's style, adding the newline msg does
// not already end with.
func (s *Stream) Println(msg string) { s.write(msg, "\n", s.style) }

// Print writes msg in the stream's style and adds no newline.
func (s *Stream) Print(msg string) { s.write(msg, "", s.style) }

// PrintlnStyled is Println with style instead of the stream's own; pass
// plain to write a message that is already colored.
func (s *Stream) PrintlnStyled(msg string, style Styler) {
	if style == nil {
		style = s.style
	}
	s.write(msg, "\n", style)
}

// write renders one message and records the first write error, which
// Println and Print cannot report; Err hands it to the caller that can.
func (s *Stream) write(msg, ending string, style Styler) {
	if ending != "" && !strings.HasSuffix(msg, ending) {
		msg += ending
	}
	if style != nil {
		msg = style(msg)
	}
	if _, err := io.WriteString(s.out, msg); err != nil && s.err == nil {
		s.err = err
	}
}

// Err reports the first error a write to the stream failed with, so that a
// broken pipe is not silently ignored.
func (s *Stream) Err() error { return s.err }

// Writer returns the writer underneath, for output that is not written as
// whole messages (a prompt, or generated source).
func (s *Stream) Writer() io.Writer { return s.out }

// Context is what a running command sees (BaseCommand's self).
type Context struct {
	// Project is the configured project the command works on.
	Project *Project
	// Env is the process environment of the run.
	Env *Env
	// Stdout and Stderr are the command's output streams.
	Stdout *Stream
	Stderr *Stream
	// Style colors the messages.
	Style *termcolors.Style
	// Prog is the program name (os.path.basename(argv[0])).
	Prog string
	// Interactive input.
	Stdin io.Reader
	// Verbosity is 0 (say only what must be said) to 3 (very verbose).
	Verbosity int
}

// Invocation is how the messages that tell the user to run another command
// spell this one, in place of Django's "python manage.py".
func (c *Context) Invocation() string {
	if c.Project != nil && c.Project.Command != "" {
		return c.Project.Command
	}
	return c.Prog
}
