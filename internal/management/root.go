package management

import (
	"errors"
	"fmt"
	"maps"
	"runtime/debug"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/termcolors"
)

// state is what every command in one invocation shares: the environment it
// runs in, the global options, and the project once it has been opened.
//
// The project is opened by the root's PersistentPreRunE rather than while
// the command tree is built, so that help and usage work without a
// database and without a settings file.
type state struct {
	env  *Env
	prog string
	// ownsProject says whether this run opened the project and must
	// therefore close it. A caller that already holds one does not.
	ownsProject bool

	projects    map[string]func() (*Project, error)
	defaultName string

	// Global options, bound to the root's persistent flags.
	settings   string
	verbose    int
	quiet      bool
	noColor    bool
	forceColor bool
	skipChecks bool

	project *Project
	ctx     *Context
}

// verbosity is the level the commands work in: 0 says only what must be
// said, 1 is normal, 2 and 3 add detail. -q asks for 0 and each -v adds
// one above normal.
func (s *state) verbosity() int {
	if s.quiet {
		return 0
	}
	return min(1+s.verbose, 3)
}

// commandNames is the sorted names of the management commands.
var commandNames = []string{
	"check", "inspectdb", "makemigrations", "migrate", "optimizemigration",
	"showmigrations", "sqlmigrate", "sqlsequencereset", "squashmigrations",
}

// CommandNames returns the sorted command names.
func CommandNames() []string { return slices.Clone(commandNames) }

// Root builds the command tree for a program invoked as prog.
//
// projects maps a settings name to a way of opening that project;
// --settings picks one and defaultName is used when it is absent. The
// chosen project is opened before a command runs and closed after it, so
// the caller does not have to.
func Root(env *Env, prog string, projects map[string]func() (*Project, error), defaultName string) *cobra.Command {
	root, _ := newRoot(env, prog, projects, defaultName)
	return root
}

func newRoot(env *Env, prog string, projects map[string]func() (*Project, error), defaultName string) (*cobra.Command, *state) {
	s := &state{env: env, prog: prog, projects: projects, defaultName: defaultName, ownsProject: true}

	root := &cobra.Command{
		Use:   prog,
		Short: "Django-style migrations for gorm",
		Long: "Manage the migrations of a gorm project: write them from the model\n" +
			"structs, apply them to a database, and inspect what is applied where.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// cobra only defaults this inside Execute, and the unknown-command
		// check below runs before that.
		SuggestionsMinimumDistance: 2,
		// Without this cobra runs the root's own RunE for an unknown
		// command; we want the suggestion and a non-zero status.
		Args: usageArgs(cobra.NoArgs),
		RunE: func(c *cobra.Command, _ []string) error { return c.Help() },
	}
	root.SetOut(env.Stdout)
	root.SetErr(env.Stderr)
	root.SetIn(env.Stdin)

	f := root.PersistentFlags()
	f.StringVar(&s.settings, "settings", "", "settings to use, as registered with gormgate.ExecuteSet")
	f.CountVarP(&s.verbose, "verbose", "v", "more detail; repeat for more still")
	f.BoolVarP(&s.quiet, "quiet", "q", false, "only say what must be said")
	f.BoolVar(&s.noColor, "no-color", false, "do not colorize the output")
	f.BoolVar(&s.forceColor, "force-color", false, "colorize the output even when it is not a terminal")
	f.BoolVar(&s.skipChecks, "skip-checks", false, "do not run the system checks first")

	root.PersistentPreRunE = s.open
	root.PersistentPostRun = func(*cobra.Command, []string) { s.close() }

	root.AddCommand(
		newMigrateCmd(s),
		newMakeMigrationsCmd(s),
		newShowMigrationsCmd(s),
		newSQLMigrateCmd(s),
		newSquashMigrationsCmd(s),
		newOptimizeMigrationCmd(s),
		newSQLSequenceResetCmd(s),
		newInspectDBCmd(s),
		newCheckCmd(s),
		newVersionCmd(s),
	)
	// A malformed flag is a usage error, not a command failure, and must
	// exit 2. cobra reports it as a plain error, so it is tagged here.
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return UsageErrorf("%s", err)
	})
	return root, s
}

// Execute runs argv (the whole command line, program name first) and
// returns the process exit status.
func Execute(env *Env, prog string, projects map[string]func() (*Project, error), defaultName string, argv []string) (code int) {
	// A few state accessors panic on an invariant a caller is supposed to
	// have checked (ProjectState.mustModel and its kin). That is a bug in
	// gormgate rather than in the project, so it is reported as one here
	// instead of reaching the runtime, which would print a goroutine dump
	// and pick its own status.
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(env.Stderr, "%s: internal error: %v\n", prog, r)
			fmt.Fprintf(env.Stderr, "%s\n", debug.Stack())
			code = ExitError
		}
	}()
	root, s := newRoot(env, prog, projects, defaultName)
	args := argv
	if len(args) > 0 {
		args = args[1:]
	}
	root.SetArgs(args)

	// cobra adds these while executing; the lookup below has to see them,
	// or "help" and "completion" would be reported as unknown.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()

	// An unknown command is a usage error. cobra reports it only once it
	// is executing, mixed in with the command's own failures, so it is
	// resolved here first, where the difference is still visible.
	if cmd, _, err := root.Find(args); err != nil || (cmd == root && len(args) > 0 && !strings.HasPrefix(args[0], "-")) {
		return reportStartupError(env, prog, unknownCommand(root, args))
	}
	err := root.Execute()
	if err == nil && s.ctx != nil {
		// A write that failed (a closed pipe, a full disk) is not visible
		// anywhere else: the streams cannot report it as they go.
		err = s.ctx.Stdout.Err()
	}
	if err == nil {
		return ExitOK
	}
	if s.ctx == nil {
		return reportStartupError(env, prog, err)
	}
	code = Report(s.ctx, err)
	var ue *UsageError
	if errors.As(err, &ue) {
		s.ctx.Stderr.Println("run '" + prog + " help' for usage")
	}
	return code
}

// unknownCommand builds the error for a first argument that names no
// command, with the closest name cobra can suggest.
func unknownCommand(root *cobra.Command, args []string) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	msg := fmt.Sprintf("unknown command %q", name)
	if sug := root.SuggestionsFor(name); len(sug) > 0 {
		msg += fmt.Sprintf("; did you mean %q?", sug[0])
	}
	return UsageErrorf("%s", msg)
}

// reportStartupError prints an error raised before a command context
// exists, so there is no style and no stream wrapper yet.
func reportStartupError(env *Env, prog string, err error) int {
	fmt.Fprintf(env.Stderr, "%s: %v\n", prog, err)
	var ue *UsageError
	if errors.As(err, &ue) {
		fmt.Fprintf(env.Stderr, "run '%s help' for usage\n", prog)
	}
	return ExitCode(err)
}

// Call runs one command in-process against a project the caller already
// holds, and returns what it failed with rather than an exit status.
func Call(p *Project, env *Env, name string, args ...string) error {
	root, s := newRoot(env, "gorm-gate", map[string]func() (*Project, error){
		"default": func() (*Project, error) { return p, nil },
	}, "default")
	s.ownsProject = false
	root.SetArgs(append([]string{name}, args...))
	if err := root.Execute(); err != nil {
		return err
	}
	if s.ctx != nil {
		return s.ctx.Stdout.Err()
	}
	return nil
}

// open resolves the settings, opens the project and builds the context the
// commands run in. It is the root's PersistentPreRunE, so it runs for
// every command but not for help.
func (s *state) open(cmd *cobra.Command, _ []string) error {
	style := termcolors.ColorStyle(false, s.env.termEnv())
	switch {
	case s.forceColor && s.noColor:
		return UsageErrorf("--no-color and --force-color cannot be used together")
	case s.forceColor:
		style = termcolors.ColorStyle(true, s.env.termEnv())
	case s.noColor:
		style = termcolors.NoStyle()
	}

	stdout := NewStream(cmd.OutOrStdout(), s.env.StdoutIsTTY)
	stderr := NewStream(cmd.ErrOrStderr(), s.env.StderrIsTTY)
	if !s.noColor {
		stderr.SetStyle(style.Error)
	}

	name := s.settings
	if name == "" {
		name = s.defaultName
	}
	openProject, ok := s.projects[name]
	if !ok {
		return Errorf("settings %q not found (available: %s)", name,
			strings.Join(slices.Sorted(maps.Keys(s.projects)), ", "))
	}
	p, err := openProject()
	if err != nil {
		return err
	}
	s.project = p
	s.ctx = &Context{
		Project:   p,
		Env:       s.env,
		Stdout:    stdout,
		Stderr:    stderr,
		Style:     style,
		Prog:      s.prog,
		Stdin:     s.env.Stdin,
		Verbosity: s.verbosity(),
	}
	return nil
}

func (s *state) close() {
	if s.project != nil && s.ownsProject {
		s.project.Close()
		s.project = nil
	}
}

// checkDatabase reports a usage error when alias is not a configured
// database. cobra has no equivalent of a choice, and the aliases are only
// known once the project is open, so every command that takes --database
// calls this first.
func (s *state) checkDatabase(alias string) error {
	if _, ok := s.project.Databases[alias]; !ok {
		return UsageErrorf("invalid --database %q (choose from %s)", alias,
			strings.Join(slices.Sorted(maps.Keys(s.project.Databases)), ", "))
	}
	return nil
}

// runChecks runs the system checks unless --skip-checks was given. A
// command that must not run them does not call it.
func (s *state) runChecks() error {
	if s.skipChecks {
		return nil
	}
	return RunChecks(s.ctx)
}

// usageArgs turns cobra's positional-argument check into a usage error, so
// that a wrong number of arguments exits 2 like any other malformed
// command line rather than 1.
func usageArgs(check cobra.PositionalArgs) cobra.PositionalArgs {
	return func(c *cobra.Command, args []string) error {
		if err := check(c, args); err != nil {
			return UsageErrorf("%s", err)
		}
		return nil
	}
}

// databaseFlag adds --database to cmd and returns the bound variable.
func databaseFlag(cmd *cobra.Command, alias *string) {
	cmd.Flags().StringVar(alias, "database", DefaultAlias,
		"the database to work on")
}

// newVersionCmd prints the version. --version on the root does the same.
func newVersionCmd(s *state) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "print the gormgate version",
		Args:  cobra.NoArgs,
		// The version is not a property of a project, so this command
		// skips opening one.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(c *cobra.Command, _ []string) error {
			fmt.Fprintln(c.OutOrStdout(), Version())
			return nil
		},
	}
}

// Version reports the gormgate version. The root package installs it; it
// is a variable because the version is read from the running program's
// build information.
var Version = func() string { return "(devel)" }
