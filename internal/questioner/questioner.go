// Package questioner answers the questions the autodetector cannot decide on
// its own: whether to write an app's first migration, what to populate
// existing rows with when a column becomes NOT NULL, and whether a
// disappeared-and-appeared field or model is really a rename.
//
// There are three implementations of Questioner. Base answers from a fixed
// set of Defaults and is what the other two build on. Interactive prompts on
// a terminal, in Django's wording byte for byte. NonInteractive refuses to
// guess: it explains why no migration can be written and asks the command to
// exit, which is what makemigrations --noinput does.
//
// A default typed at the Interactive prompt is Go source, not a value, so it
// is returned as a migrations.GoExpr and type-checked against the field
// before being accepted (see CheckExpr).
//
// django: db/migrations/questioner.py
package questioner

import (
	"bufio"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	m "github.com/ctolon/gormgate/migrations"
)

// The two ways a question can fail to produce an answer. They are
// sentinels rather than an error carrying an exit status: what a shell
// should make of them is the command layer's decision, not the
// questioner's.
var (
	// ErrQuit reports that the user ended the prompt, either by closing
	// the input or by asking to quit. They have already been told so on
	// screen, so the command stops without explaining itself again.
	ErrQuit = errors.New("cancelled")
	// ErrNoAnswer reports that the question could not be answered: the
	// user chose to handle it in their model structs, or nothing was
	// allowed to ask because the run is not interactive.
	ErrNoAnswer = errors.New("no answer available")
)

// NotProvided marks a field whose NULL rows the user chose to handle
// manually: the autodetector leaves the field's default alone instead of
// writing a one-off default into the migration. Compare it by identity.
//
// django: db/models/fields/__init__.py NOT_PROVIDED
var NotProvided = &m.GoExpr{Source: "<NOT_PROVIDED>"}

// A Questioner answers the questions the autodetector cannot decide on its
// own. Implementations either prompt the user or return fixed answers.
//
// django: questioner.py MigrationQuestioner
type Questioner interface {
	// AskInitial reports whether to create an initial migration for app.
	AskInitial(app string) bool
	// AskNotNullAddition returns the one-off default to populate existing
	// rows with when a non-nullable field is added; nil means none was
	// given.
	AskNotNullAddition(field, model string) (*m.GoExpr, error)
	// AskNotNullAlteration returns the one-off default to populate
	// existing NULL rows with when a field becomes non-nullable, nil when
	// none was given, or NotProvided to leave them to the user.
	AskNotNullAlteration(field, model string) (*m.GoExpr, error)
	// AskRename reports whether oldName was renamed to newName on model.
	AskRename(model, oldName, newName string, f m.Field) (bool, error)
	// AskRenameModel reports whether old was renamed to new.
	AskRenameModel(old, new *m.ModelState) (bool, error)
	// AskMerge reports whether app's conflicting migrations may be merged.
	AskMerge(app string) (bool, error)
	// AskAutoNowAddAddition returns the one-off default for a newly added
	// autoCreateTime field; nil means none was given.
	AskAutoNowAddAddition(field, model string) (*m.GoExpr, error)
	// AskUniqueCallableDefaultAddition confirms adding a unique field
	// whose default is a function, which will not produce unique values.
	AskUniqueCallableDefaultAddition(field, model string) error
}

// MigrationsDir reports where an app's migrations live. It sets disabled
// when MIGRATION_MODULES maps the app to None, and clears installed when the
// app is not installed at all.
type MigrationsDir func(app string) (dir string, disabled bool, installed bool)

// Defaults are the answers Base gives to the yes/no questions. The zero
// value answers no to all of them.
type Defaults struct {
	Initial     bool
	Rename      bool
	RenameModel bool
	Merge       bool
}

// Base answers every question without prompting: the yes/no questions from
// Defaults, and the one-off default questions with "no default given".
// Interactive and NonInteractive embed it and override what they ask about.
//
// django: questioner.py MigrationQuestioner
type Base struct {
	// Defaults are the fixed answers to the yes/no questions.
	Defaults Defaults
	// SpecifiedApps are the app labels named on the command line; they
	// always get an initial migration.
	SpecifiedApps map[string]bool
	// DryRun suppresses the prompts that would produce a one-off default.
	DryRun bool
	// Dirs locates an app's migrations; when nil, AskInitial falls back to
	// Defaults.Initial.
	Dirs MigrationsDir
}

var migrationFile = regexp.MustCompile(`^\d{4}_\w*\.go$`)

// AskInitial decides whether to create an initial migration for app.
//
// django: questioner.py MigrationQuestioner.ask_initial
func (q *Base) AskInitial(app string) bool {
	if q.SpecifiedApps[app] {
		return true
	}
	if q.Dirs == nil {
		return q.Defaults.Initial
	}
	dir, disabled, installed := q.Dirs(app)
	if !installed || disabled {
		return q.Defaults.Initial
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return q.Defaults.Initial
	}
	// An app whose migrations package exists but holds no migration files
	// gets an initial migration.
	for _, e := range entries {
		if migrationFile.MatchString(e.Name()) {
			return false
		}
	}
	return true
}

// AskNotNullAddition reports that no one-off default was given.
func (q *Base) AskNotNullAddition(string, string) (*m.GoExpr, error) { return nil, nil }

// AskNotNullAlteration reports that no one-off default was given.
func (q *Base) AskNotNullAlteration(string, string) (*m.GoExpr, error) { return nil, nil }

// AskRename returns Defaults.Rename.
func (q *Base) AskRename(string, string, string, m.Field) (bool, error) {
	return q.Defaults.Rename, nil
}

// AskRenameModel returns Defaults.RenameModel.
func (q *Base) AskRenameModel(*m.ModelState, *m.ModelState) (bool, error) {
	return q.Defaults.RenameModel, nil
}

// AskMerge returns Defaults.Merge.
func (q *Base) AskMerge(string) (bool, error) { return q.Defaults.Merge, nil }

// AskAutoNowAddAddition reports that no one-off default was given.
func (q *Base) AskAutoNowAddAddition(string, string) (*m.GoExpr, error) { return nil, nil }

// AskUniqueCallableDefaultAddition accepts the field without comment.
func (q *Base) AskUniqueCallableDefaultAddition(string, string) error { return nil }

// Interactive prompts the user on Out and reads the answers from In.
//
// django: questioner.py InteractiveMigrationQuestioner
type Interactive struct {
	Base
	Out io.Writer
	In  *bufio.Reader
	// FieldLookup lets prompts type-check defaults against the field being
	// added or altered.
	FieldLookup func(model, field string) *m.Field
}

// NewInteractive builds an interactive questioner reading from in.
func NewInteractive(base Base, out io.Writer, in io.Reader) *Interactive {
	return &Interactive{Base: base, Out: out, In: bufio.NewReader(in)}
}

// errEndOfInput is returned when the input ends mid-prompt, for instance on
// Ctrl-D. It is the Go stand-in for the EOFError Python's input() raises.
var errEndOfInput = errors.New("EOF when reading a line")

func (q *Interactive) write(s string, ending string) {
	fmt.Fprint(q.Out, s+ending)
}

// readLine reads one answer, stripping the line ending.
func (q *Interactive) readLine() (string, error) {
	line, err := q.In.ReadString('\n')
	if err != nil && line == "" {
		return "", errEndOfInput
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// readLineOrCancel reads one answer and, when the input ends, cancels the
// prompt: it prints "Cancelled." and reports exit status 1. End of input
// stands in for the KeyboardInterrupt Django catches around input().
//
// django: questioner.py InteractiveMigrationQuestioner._choice_input and
// _ask_default (except KeyboardInterrupt: write("\nCancelled."); sys.exit(1))
func (q *Interactive) readLineOrCancel() (string, error) {
	line, err := q.readLine()
	if err != nil {
		q.write("\ncancelled", "\n")
		return "", ErrQuit
	}
	return line, nil
}

// booleanInput asks a yes/no question, repeating it until the answer starts
// with y or n. An empty answer takes def when one is given.
//
// django: questioner.py InteractiveMigrationQuestioner._boolean_input
func (q *Interactive) booleanInput(question string, def *bool) (bool, error) {
	q.write(question+" ", "")
	result, err := q.readLine()
	if err != nil {
		return false, err
	}
	if result == "" && def != nil {
		return *def, nil
	}
	for result == "" || !strings.ContainsRune("yn", []rune(strings.ToLower(result[:1]))[0]) {
		q.write("answer yes or no: ", "")
		if result, err = q.readLine(); err != nil {
			return false, err
		}
	}
	return strings.ToLower(result[:1]) == "y", nil
}

// choiceInput prints a numbered menu and returns the 1-based choice.
//
// django: questioner.py InteractiveMigrationQuestioner._choice_input
func (q *Interactive) choiceInput(question string, choices []string) (int, error) {
	q.write(question, "\n")
	for i, c := range choices {
		q.write(fmt.Sprintf(" %d) %s", i+1, c), "\n")
	}
	q.write("choose: ", "")
	for {
		result, err := q.readLineOrCancel()
		if err != nil {
			return 0, err
		}
		if v, err := strconv.Atoi(strings.TrimSpace(result)); err == nil && v > 0 && v <= len(choices) {
			return v, nil
		}
		q.write("choose one of the numbers above: ", "")
	}
}

// askDefault prompts for a Go expression.
//
// django: questioner.py InteractiveMigrationQuestioner._ask_default
func (q *Interactive) askDefault(f *m.Field, def string) (*m.GoExpr, error) {
	q.write("enter the default as a Go expression", "\n")
	if def != "" {
		q.write(fmt.Sprintf("press Enter to take %s, or type another expression", def), "\n")
	}
	q.write("the time package is available, so time.Now works as a value", "\n")
	q.write("type 'exit' to give up", "\n")
	for {
		prompt := ">>> "
		if def != "" {
			prompt = fmt.Sprintf("[default: %s] >>> ", def)
		}
		q.write(prompt, "")
		code, err := q.readLineOrCancel()
		if err != nil {
			return nil, err
		}
		if code == "" && def != "" {
			code = def
		}
		switch {
		case code == "":
			q.write("enter an expression, or 'exit' to give up", "\n")
		case code == "exit":
			return nil, ErrQuit
		default:
			if err := CheckExpr(code, f); err != nil {
				q.write(err.Error(), "\n")
				continue
			}
			return &m.GoExpr{Source: code}, nil
		}
	}
}

// AskNotNullAddition offers a one-off default for a newly added
// non-nullable field, or quitting.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_not_null_addition
func (q *Interactive) AskNotNullAddition(field, model string) (*m.GoExpr, error) {
	if q.DryRun {
		return nil, nil
	}
	choice, err := q.choiceInput(
		fmt.Sprintf("%s.%s is not nullable and the rows already there need a value for it.", model, field),
		[]string{
			"set a one-off default now, on the rows that have none",
			"stop, and give the field a default in the model struct",
		})
	if err != nil {
		return nil, err
	}
	if choice == 2 {
		return nil, ErrNoAnswer
	}
	return q.askDefault(q.fieldFor(model, field), "")
}

// AskNotNullAlteration offers a one-off default for the NULL rows of a
// field becoming non-nullable, handling them manually, or quitting.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_not_null_alteration
func (q *Interactive) AskNotNullAlteration(field, model string) (*m.GoExpr, error) {
	if q.DryRun {
		return nil, nil
	}
	choice, err := q.choiceInput(
		fmt.Sprintf("%s.%s is becoming non-nullable and the rows already there need a value for it.", model, field),
		[]string{
			"set a one-off default now, on the rows that have none",
			"Ignore for now. Existing rows that contain NULL values will have to be handled manually, for example with a RunGo or RunSQL operation.",
			"stop, and give the field a default in the model struct",
		})
	if err != nil {
		return nil, err
	}
	switch choice {
	case 2:
		return NotProvided, nil
	case 3:
		return nil, ErrNoAnswer
	}
	return q.askDefault(q.fieldFor(model, field), "")
}

// AskRename asks whether a field was renamed.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_rename
func (q *Interactive) AskRename(model, oldName, newName string, f m.Field) (bool, error) {
	no := false
	return q.booleanInput(fmt.Sprintf("Was %s.%s renamed to %s.%s (a %s)? [y/N]", model, oldName, model, newName, f.ClassName()), &no)
}

// AskRenameModel asks whether a model was renamed.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_rename_model
func (q *Interactive) AskRenameModel(old, new *m.ModelState) (bool, error) {
	no := false
	return q.booleanInput(fmt.Sprintf("Was the model %s.%s renamed to %s? [y/N]", old.App, old.Name, new.Name), &no)
}

// AskMerge asks whether the conflicting migration branches may be merged.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_merge
func (q *Interactive) AskMerge(string) (bool, error) {
	no := false
	return q.booleanInput("\nMerging will only work if the operations printed above do not conflict\n"+
		"with each other (working on different fields or models)\n"+
		"Should these migration branches be merged? [y/N]", &no)
}

// AskAutoNowAddAddition offers a one-off default for a newly added
// autoCreateTime field, or quitting.
//
// django: questioner.py InteractiveMigrationQuestioner.ask_auto_now_add_addition
func (q *Interactive) AskAutoNowAddAddition(field, model string) (*m.GoExpr, error) {
	if q.DryRun {
		return nil, nil
	}
	choice, err := q.choiceInput(
		fmt.Sprintf("%s.%s has autoCreateTime and the rows already there need a value for it.", model, field),
		[]string{
			"set a one-off default now, on every existing row",
			"stop, and give the field a default in the model struct",
		})
	if err != nil {
		return nil, err
	}
	if choice == 2 {
		return nil, ErrNoAnswer
	}
	return q.askDefault(q.fieldFor(model, field), "time.Now")
}

// AskUniqueCallableDefaultAddition warns that a function default on a
// unique field will not produce unique values, and offers quitting.
//
// django: questioner.py
// InteractiveMigrationQuestioner.ask_unique_callable_default_addition
func (q *Interactive) AskUniqueCallableDefaultAddition(field, model string) error {
	if q.DryRun {
		return nil
	}
	choice, err := q.choiceInput(
		fmt.Sprintf("%s.%s is unique and its default is a function, which will give every existing row the same value.", model, field),
		[]string{
			"write the migration anyway, and fill the column in by hand afterwards",
			"stop, and change the field in the model struct",
		})
	if err != nil {
		return err
	}
	if choice == 2 {
		return ErrNoAnswer
	}
	return nil
}

func (q *Interactive) fieldFor(model, field string) *m.Field {
	if q.FieldLookup == nil {
		return nil
	}
	return q.FieldLookup(model, field)
}

// NonInteractive answers like Base but explains on Log why a field could
// not be migrated, and refuses rather than inventing a default.
//
// django: questioner.py NonInteractiveMigrationQuestioner
type NonInteractive struct {
	Base
	// Verbosity silences logLackOfMigration when it is zero.
	Verbosity int
	// Log writes one explanatory line.
	Log func(string)
}

// django: questioner.py NonInteractiveMigrationQuestioner.log_lack_of_migration
func (q *NonInteractive) logLackOfMigration(field, model, reason string) {
	if q.Verbosity > 0 {
		q.Log(fmt.Sprintf("%s.%s not migrated: %s", model, field, reason))
	}
}

// AskNotNullAddition logs why the field was skipped and quits.
//
// django: questioner.py
// NonInteractiveMigrationQuestioner.ask_not_null_addition
func (q *NonInteractive) AskNotNullAddition(field, model string) (*m.GoExpr, error) {
	q.logLackOfMigration(field, model, "it is impossible to add a non-nullable field without specifying a default")
	return nil, ErrNoAnswer
}

// AskNotNullAlteration logs that the field needs correcting and returns
// NotProvided.
//
// django: questioner.py
// NonInteractiveMigrationQuestioner.ask_not_null_alteration
func (q *NonInteractive) AskNotNullAlteration(field, model string) (*m.GoExpr, error) {
	q.Log(fmt.Sprintf("Field '%s' on model '%s' given a default of NOT PROVIDED and must be corrected.", field, model))
	return NotProvided, nil
}

// AskAutoNowAddAddition logs why the field was skipped and quits.
//
// django: questioner.py
// NonInteractiveMigrationQuestioner.ask_auto_now_add_addition
func (q *NonInteractive) AskAutoNowAddAddition(field, model string) (*m.GoExpr, error) {
	q.logLackOfMigration(field, model, "it is impossible to add a field with 'autoCreateTime' without specifying a default")
	return nil, ErrNoAnswer
}

// CheckExpr parses and type-checks a default entered at the prompt. The
// expression may use the time package; when the field's Go type T is known it
// must be assignable to T, or be a function returning T (a callable
// default).
func CheckExpr(code string, f *m.Field) error {
	if _, err := parser.ParseExpr(code); err != nil {
		return fmt.Errorf("syntax error: %v", strings.TrimPrefix(err.Error(), "1:"))
	}
	target := goTypeOf(f)
	if target == "" {
		return typeCheck("var _ = " + code)
	}
	err := typeCheck("var _ " + target + " = " + code)
	if err == nil {
		return nil
	}
	if typeCheck("var _ func() "+target+" = "+code) == nil {
		return nil
	}
	return err
}

// newImporter builds the importer typeCheck uses. It reads the standard
// library from source, which needs a Go toolchain on PATH; it is a variable
// so that a test can supply one that fails.
var newImporter = func(fset *token.FileSet) types.Importer {
	return importer.ForCompiler(fset, "source", nil)
}

// recordingImporter remembers whether the importer itself failed, which is
// what happens when there is no Go toolchain on PATH: the "source" importer
// shells out to `go list`. That is a failure of the check, not of the
// expression the user typed.
type recordingImporter struct {
	imp types.Importer
	err error
}

func (r *recordingImporter) Import(path string) (*types.Package, error) {
	pkg, err := r.imp.Import(path)
	if err != nil && r.err == nil {
		r.err = err
	}
	return pkg, err
}

// typeCheck type-checks a declaration built around the entered expression.
//
// The time package is only imported when the expression names it, both
// because importing from source is slow and because it needs a Go toolchain
// on PATH; an expression that does not mention time is checked with no
// importer at all. When the importer does run and fails, the declaration has
// still parsed, and that is all typeCheck can report: it accepts the
// expression rather than blaming the user for a missing toolchain.
func typeCheck(decl string) error {
	src := "package p\n"
	if strings.Contains(decl, "time.") {
		// var _ = time.Now keeps the import used even when the
		// expression only mentions time inside a string literal.
		src += "import \"time\"\nvar _ = time.Now\n"
	}
	src += decl + "\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "default.go", src, 0)
	if err != nil {
		return fmt.Errorf("syntax error: %v", err)
	}
	imp := &recordingImporter{imp: newImporter(fset)}
	conf := types.Config{Importer: imp}
	_, checkErr := conf.Check("p", fset, []*ast.File{file}, nil)
	if checkErr == nil || imp.err != nil {
		return nil
	}
	var terr types.Error
	if errors.As(checkErr, &terr) {
		return fmt.Errorf("type error: %s", terr.Msg)
	}
	return fmt.Errorf("type error: %v", checkErr)
}

// goTypeOf returns the Go type a default for f must have, or "" when it
// can't be expressed without importing the model's packages.
func goTypeOf(f *m.Field) string {
	if f == nil || f.Custom != nil {
		return ""
	}
	switch f.Type {
	case m.Bool:
		return "bool"
	case m.Int:
		return "int64"
	case m.Uint:
		return "uint64"
	case m.Float:
		return "float64"
	case m.String:
		return "string"
	case m.Time:
		return "time.Time"
	case m.Bytes:
		return "[]byte"
	}
	return ""
}
