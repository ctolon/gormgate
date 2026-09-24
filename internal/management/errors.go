package management

import (
	"errors"
	"fmt"

	"github.com/ctolon/gormgate/internal/questioner"
)

// Exit statuses. A command reports failure by returning an error; ExitCode
// turns that error into one of these.
const (
	// ExitOK is a command that did what it was asked.
	ExitOK = 0
	// ExitError is a command that could not.
	ExitError = 1
	// ExitUsage is a command line the parser rejected.
	ExitUsage = 2
	// ExitAbort is a command that needed an answer it was not allowed to
	// ask for, because it was told not to prompt.
	ExitAbort = 3
)

// Error reports that a command could not do what it was asked: the
// ordinary failure of a command, reported on stderr and exiting
// ExitError.
type Error struct{ msg string }

// Errorf builds an Error.
func Errorf(format string, a ...any) error {
	return &Error{msg: fmt.Sprintf(format, a...)}
}

func (e *Error) Error() string { return e.msg }

// UsageError reports a command line the parser rejected. It exits
// ExitUsage, and the caller prints the command's usage after it.
type UsageError struct{ msg string }

// UsageErrorf builds a UsageError.
func UsageErrorf(format string, a ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, a...)}
}

func (e *UsageError) Error() string { return e.msg }

// AbortError reports that a command stopped because it needed an answer
// and was told not to prompt for one. It exits ExitAbort so that a script
// can tell "it needed input" from "it failed".
type AbortError struct{ msg string }

// AbortErrorf builds an AbortError.
func AbortErrorf(format string, a ...any) error {
	return &AbortError{msg: fmt.Sprintf(format, a...)}
}

func (e *AbortError) Error() string { return e.msg }

// silentError ends a command with a status it has already explained on its
// own output. It carries no message, so the caller prints nothing; it is
// what a command returns after writing its own diagnosis.
type silentError struct {
	code int
	// why is not printed. It records, for a reader of the code, what the
	// command wrote instead.
	why string
}

// Silent ends the command with code, printing nothing. why says what the
// command has already written.
func Silent(code int, why string) error {
	return &silentError{code: code, why: why}
}

func (e *silentError) Error() string { return e.why }

// ExitCode reports the process exit status for err.
//
// A questioner error is mapped here rather than at the point it is raised:
// the questioner knows it could not get an answer, and this is the only
// place that has to decide what that means to a shell.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var se *silentError
	if errors.As(err, &se) {
		return se.code
	}
	var ue *UsageError
	if errors.As(err, &ue) {
		return ExitUsage
	}
	var ae *AbortError
	if errors.As(err, &ae) {
		return ExitAbort
	}
	if errors.Is(err, questioner.ErrNoAnswer) {
		return ExitAbort
	}
	return ExitError
}

// Report writes err to stderr the way a Go command line tool does, as
// "prog: message", and returns the exit status. A silentError prints
// nothing, because the command has already said what went wrong.
func Report(c *Context, err error) int {
	if err == nil {
		return ExitOK
	}
	var se *silentError
	if errors.As(err, &se) {
		return se.code
	}
	var sce *SystemCheckError
	if errors.As(err, &sce) {
		// The check report is already formatted, headings and all.
		c.Stderr.PrintlnStyled(sce.msg, plain)
		return ExitError
	}
	c.Stderr.Println(c.Prog + ": " + err.Error())
	return ExitCode(err)
}

// SystemCheckError reports that the system checks found errors. Its
// message is the whole report, already formatted.
type SystemCheckError struct{ msg string }

func (e *SystemCheckError) Error() string { return e.msg }
