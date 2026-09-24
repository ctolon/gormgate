package management

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// testEnv is a non-terminal environment with captured streams and no
// environment variables, so that nothing the developer's shell exports can
// change the output.
func testEnv(stdin string) (*Env, *bytes.Buffer, *bytes.Buffer) {
	var out, errOut bytes.Buffer
	env := &Env{
		Stdout: &out, Stderr: &errOut, Stdin: strings.NewReader(stdin),
		StdoutIsTTY: func() bool { return false },
		StderrIsTTY: func() bool { return false },
		StdinIsTTY:  func() bool { return false },
		Getenv:      func(string) (string, bool) { return "", false },
		Getwd:       func() (string, error) { return "/proj", nil },
	}
	return env, &out, &errOut
}

// testProject is a project with one app and no databases: enough to build
// the command tree, never enough to reach a database.
func testProject() *Project {
	return &Project{Apps: []*App{{Label: "blog"}}, Command: "gorm-gate"}
}

// run builds the command tree over testProject and runs argv against it.
func run(stdin string, argv ...string) (code int, stdout, stderr string) {
	env, out, errOut := testEnv(stdin)
	projects := map[string]func() (*Project, error){
		"default": func() (*Project, error) { return testProject(), nil },
	}
	code = Execute(env, "gorm-gate", projects, "default", append([]string{"gorm-gate"}, argv...))
	return code, out.String(), errOut.String()
}

// runIn runs argv against p, writing into env's streams, and returns the
// exit status.
func runIn(env *Env, p *Project, argv ...string) int {
	projects := map[string]func() (*Project, error){
		"default": func() (*Project, error) { return p, nil },
	}
	return Execute(env, "gorm-gate", projects, "default", append([]string{"gorm-gate"}, argv...))
}

// sub finds a subcommand of a freshly built tree.
func sub(t *testing.T, name string) *cobra.Command {
	t.Helper()
	env, _, _ := testEnv("")
	root := Root(env, "gorm-gate", nil, "default")
	for _, c := range root.Commands() {
		if c.Name() == name {
			return c
		}
	}
	t.Fatalf("no command named %q", name)
	return nil
}

func TestCommandNames(t *testing.T) {
	env, _, _ := testEnv("")
	root := Root(env, "gorm-gate", nil, "default")
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, n := range CommandNames() {
		if !have[n] {
			t.Errorf("the tree has no %q command", n)
		}
	}
	if !have["version"] {
		t.Error("the tree has no version command")
	}
}

// TestFlagSurface pins the option each command offers, by name. It is the
// contract a script written against gorm-gate depends on; the help text
// around it is free to change.
func TestFlagSurface(t *testing.T) {
	for _, tc := range []struct {
		cmd   string
		flags []string
	}{
		{"migrate", []string{"database", "no-input", "fake", "fake-initial", "plan", "run-syncdb", "check", "prune"}},
		{"makemigrations", []string{"dry-run", "merge", "empty", "no-input", "name", "no-header", "check", "scriptable", "update"}},
		{"showmigrations", []string{"database", "plan"}},
		{"sqlmigrate", []string{"database", "backwards"}},
		{"squashmigrations", []string{"no-optimize", "no-input", "squashed-name", "no-header"}},
		{"optimizemigration", []string{"check"}},
		{"sqlsequencereset", []string{"database"}},
		{"inspectdb", []string{"database", "include-partitions", "include-views"}},
		{"check", []string{"tag", "list-tags", "deploy", "fail-level", "database"}},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			c := sub(t, tc.cmd)
			for _, f := range tc.flags {
				if c.Flags().Lookup(f) == nil {
					t.Errorf("%s has no --%s", tc.cmd, f)
				}
			}
		})
	}
}

// TestGlobalFlags checks that the options every command shares are on the
// root, so they work wherever they are written.
func TestGlobalFlags(t *testing.T) {
	env, _, _ := testEnv("")
	root := Root(env, "gorm-gate", nil, "default")
	for _, f := range []string{"settings", "verbose", "quiet", "no-color", "force-color", "skip-checks"} {
		if root.PersistentFlags().Lookup(f) == nil {
			t.Errorf("the root has no --%s", f)
		}
	}
}

// TestVerbosity pins how -q and repeated -v map onto the levels the
// commands work in.
func TestVerbosity(t *testing.T) {
	for _, tc := range []struct {
		name string
		s    state
		want int
	}{
		{"default", state{}, 1},
		{"quiet", state{quiet: true}, 0},
		{"-v", state{verbose: 1}, 2},
		{"-vv", state{verbose: 2}, 3},
		{"-vvv caps", state{verbose: 3}, 3},
		{"quiet wins", state{quiet: true, verbose: 2}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.s.verbosity(); got != tc.want {
				t.Errorf("verbosity = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestUnknownSettings(t *testing.T) {
	code, _, errOut := run("", "migrate", "--settings", "nope")
	if code != ExitError {
		t.Errorf("exit status = %d, want %d", code, ExitError)
	}
	if !strings.Contains(errOut, `settings "nope" not found`) {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestUnknownCommandSuggests(t *testing.T) {
	code, _, errOut := run("", "migratte")
	if code != ExitUsage {
		t.Errorf("exit status = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, `unknown command "migratte"`) {
		t.Errorf("stderr = %q", errOut)
	}
	if !strings.Contains(errOut, `did you mean "migrate"?`) {
		t.Errorf("no suggestion in %q", errOut)
	}
}

func TestColorOptionsConflict(t *testing.T) {
	code, _, errOut := run("", "migrate", "--no-color", "--force-color")
	if code != ExitUsage {
		t.Errorf("exit status = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "cannot be used together") {
		t.Errorf("stderr = %q", errOut)
	}
}

func TestUnknownFlagIsAUsageError(t *testing.T) {
	code, _, errOut := run("", "migrate", "--nosuchflag")
	if code != ExitUsage {
		t.Errorf("exit status = %d, want %d", code, ExitUsage)
	}
	if !strings.Contains(errOut, "unknown flag") {
		t.Errorf("stderr = %q", errOut)
	}
}

// TestExitCode pins what each kind of failure means to a shell. The
// distinction is the part of the interface scripts depend on.
func TestExitCode(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, ExitOK},
		{"command failure", Errorf("it broke"), ExitError},
		{"usage", UsageErrorf("bad flag"), ExitUsage},
		{"abort", AbortErrorf("needed an answer"), ExitAbort},
		{"silent keeps its status", Silent(ExitUsage, "already said"), ExitUsage},
		{"wrapped", fmt.Errorf("while migrating: %w", AbortErrorf("x")), ExitAbort},
		{"plain error", errors.New("boom"), ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExitCode(tc.err); got != tc.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}

// TestReportSilentPrintsNothing checks that a command which has already
// explained itself is not contradicted by a second message.
func TestReportSilentPrintsNothing(t *testing.T) {
	env, _, errOut := testEnv("")
	c := &Context{Env: env, Stderr: NewStream(env.Stderr, env.StderrIsTTY), Prog: "gorm-gate"}
	if got := Report(c, Silent(ExitError, "the command said so itself")); got != ExitError {
		t.Errorf("status = %d, want %d", got, ExitError)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}
}

// TestReportNamesTheProgram checks the "prog: message" shape Go command
// line tools use.
func TestReportNamesTheProgram(t *testing.T) {
	env, _, errOut := testEnv("")
	c := &Context{Env: env, Stderr: NewStream(env.Stderr, env.StderrIsTTY), Prog: "gorm-gate"}
	Report(c, Errorf("it broke"))
	if got, want := errOut.String(), "gorm-gate: it broke\n"; got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

// failingWriter reports an error on every write, as a closed pipe does.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("broken pipe") }

// TestStreamRecordsWriteError checks that output a command believed it had
// written, but had not, is not silently lost.
func TestStreamRecordsWriteError(t *testing.T) {
	s := NewStream(failingWriter{}, func() bool { return false })
	s.Println("hello")
	s.Println("again")
	if s.Err() == nil {
		t.Fatal("the stream swallowed a write error")
	}
	if got := s.Err().Error(); got != "broken pipe" {
		t.Errorf("err = %q, want the first one", got)
	}
}

func TestStreamAddsOneNewline(t *testing.T) {
	var b bytes.Buffer
	s := NewStream(&b, func() bool { return false })
	s.Println("a")
	s.Println("b\n")
	s.Print("c")
	if got, want := b.String(), "a\nb\nc"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}

func TestStreamStyleAppliesToPrintln(t *testing.T) {
	var b bytes.Buffer
	s := NewStream(&b, func() bool { return true })
	s.SetStyle(func(msg string) string { return "<" + msg + ">" })
	s.Println("x")
	if got, want := b.String(), "<x\n>"; got != want {
		t.Errorf("wrote %q, want %q", got, want)
	}
}
