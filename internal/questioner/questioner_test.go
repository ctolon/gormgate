package questioner

import (
	"bytes"
	"errors"
	"fmt"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	m "github.com/ctolon/gormgate/migrations"
)

// django: tests/migrations/test_questioner.py
//
// Django's questioner prompts for a Python expression and evaluates it with
// eval(); gormgate prompts for a GO expression and type-checks it with
// go/types (questioner.CheckExpr). Every _ask_default test below is ported
// to that equivalent: "datetime.timedelta(days=1)" becomes a Go expression,
// NameError/AttributeError become go/types "undefined: ..." errors, which
// gormgate reports as TypeError.

// newTestInteractive builds an Interactive questioner reading the given
// lines and writing to a buffer, the Go equivalent of Django's
// mock.patch("builtins.input") plus OutputWrapper(StringIO()).
func newTestInteractive(base Base, input ...string) (*Interactive, *bytes.Buffer) {
	var out bytes.Buffer
	in := ""
	if len(input) > 0 {
		in = strings.Join(input, "\n") + "\n"
	}
	return NewInteractive(base, &out, strings.NewReader(in)), &out
}

// assertExit checks that a prompt gave up in the expected way: want is
// ErrQuit when the user ended it and ErrNoAnswer when the question could
// not be answered.
func assertExit(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("expected %v, got %v (%T)", want, err, err)
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output does not contain %q\n--- output ---\n%s", want, got)
	}
}

// ---------------------------------------------------------------------------
// QuestionerTests
// ---------------------------------------------------------------------------

// django: tests/migrations/test_questioner.py QuestionerTests.test_ask_initial_with_disabled_migrations
//
// Django disables migrations with MIGRATION_MODULES = {"migrations": None};
// gormgate reports that through the MigrationsDir callback's disabled flag.
func TestQuestioner_AskInitialWithDisabledMigrations(t *testing.T) {
	q := &Base{Dirs: func(app string) (string, bool, bool) {
		return "", true, true // disabled
	}}
	if q.AskInitial("migrations") {
		t.Fatal("AskInitial should be false for an app with migrations disabled")
	}
}

// django: tests/migrations/test_questioner.py QuestionerTests.test_ask_not_null_alteration
func TestQuestioner_AskNotNullAlteration(t *testing.T) {
	q := &Base{}
	got, err := q.AskNotNullAlteration("field_name", "model_name")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil (Django's None), got %v", got)
	}
}

// django: tests/migrations/test_questioner.py QuestionerTests.test_ask_not_null_alteration_not_provided
func TestQuestioner_AskNotNullAlterationNotProvided(t *testing.T) {
	q, _ := newTestInteractive(Base{}, "2")
	got, err := q.AskNotNullAlteration("field_name", "model_name")
	if err != nil {
		t.Fatal(err)
	}
	if got != NotProvided {
		t.Fatalf("expected NotProvided (Django's NOT_PROVIDED), got %v", got)
	}
}

// AskInitial's remaining branches. Django's ask_initial inspects the app's
// migrations package for .py files; gormgate inspects the directory for
// files matching ^\d{4}_\w*\.go$.
//
// django: db/migrations/questioner.py MigrationQuestioner.ask_initial
func TestQuestioner_AskInitial(t *testing.T) {
	dirWith := func(t *testing.T, names ...string) string {
		t.Helper()
		dir := t.TempDir()
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, n), []byte("package migrations\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	empty := dirWith(t)
	withMigrations := dirWith(t, "0001_initial.go")
	// Only files that look like migrations count; a helper file does not.
	withHelperOnly := dirWith(t, "migrations.go", "_util.go")

	tests := []struct {
		name string
		q    *Base
		app  string
		want bool
	}{
		{
			// Specified on the command line: definitely true.
			name: "specified_apps",
			q:    &Base{SpecifiedApps: map[string]bool{"blog": true}},
			app:  "blog", want: true,
		},
		{
			// A fake app (not installed) falls back to the default.
			name: "not_installed",
			q:    &Base{Dirs: func(string) (string, bool, bool) { return "", false, false }},
			app:  "blog", want: false,
		},
		{
			name: "not_installed_default_true",
			q: &Base{
				Defaults: Defaults{Initial: true},
				Dirs:     func(string) (string, bool, bool) { return "", false, false },
			},
			app: "blog", want: true,
		},
		{
			// Missing directory: fall back to the default.
			name: "missing_dir",
			q:    &Base{Dirs: func(string) (string, bool, bool) { return "/nonexistent-gormgate", false, true }},
			app:  "blog", want: false,
		},
		{
			name: "empty_migrations_package",
			q:    &Base{Dirs: func(string) (string, bool, bool) { return empty, false, true }},
			app:  "blog", want: true,
		},
		{
			name: "package_with_helpers_only",
			q:    &Base{Dirs: func(string) (string, bool, bool) { return withHelperOnly, false, true }},
			app:  "blog", want: true,
		},
		{
			name: "package_with_migrations",
			q:    &Base{Dirs: func(string) (string, bool, bool) { return withMigrations, false, true }},
			app:  "blog", want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.q.AskInitial(tt.app); got != tt.want {
				t.Fatalf("AskInitial(%q) = %v, want %v", tt.app, got, tt.want)
			}
		})
	}
}

// django: db/migrations/questioner.py MigrationQuestioner (the remaining
// non-interactive defaults, which Django exercises through the autodetector
// tests).
func TestQuestioner_BaseDefaults(t *testing.T) {
	q := &Base{}
	if v, err := q.AskNotNullAddition("f", "M"); v != nil || err != nil {
		t.Fatalf("AskNotNullAddition = %v, %v; want nil, nil", v, err)
	}
	if v, err := q.AskAutoNowAddAddition("f", "M"); v != nil || err != nil {
		t.Fatalf("AskAutoNowAddAddition = %v, %v; want nil, nil", v, err)
	}
	if err := q.AskUniqueCallableDefaultAddition("f", "M"); err != nil {
		t.Fatalf("AskUniqueCallableDefaultAddition = %v; want nil", err)
	}
	if v, _ := q.AskRename("M", "a", "b", m.Field{}); v {
		t.Fatal("AskRename should default to false")
	}
	if v, _ := q.AskRenameModel(&m.ModelState{}, &m.ModelState{}); v {
		t.Fatal("AskRenameModel should default to false")
	}
	if v, _ := q.AskMerge("app"); v {
		t.Fatal("AskMerge should default to false")
	}

	yes := &Base{Defaults: Defaults{Rename: true, RenameModel: true, Merge: true}}
	if v, _ := yes.AskRename("M", "a", "b", m.Field{}); !v {
		t.Fatal("AskRename should honour defaults")
	}
	if v, _ := yes.AskRenameModel(&m.ModelState{}, &m.ModelState{}); !v {
		t.Fatal("AskRenameModel should honour defaults")
	}
	if v, _ := yes.AskMerge("app"); !v {
		t.Fatal("AskMerge should honour defaults")
	}
}

// ---------------------------------------------------------------------------
// QuestionerHelperMethodsTests
// ---------------------------------------------------------------------------

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_default_timedelta
//
// Django enters "datetime.timedelta(days=1)" and gets a timedelta back;
// gormgate keeps the Go source of the expression in a GoExpr, which the
// writer emits verbatim into the migration file.
func TestQuestioner_QuestionerDefaultTimedelta(t *testing.T) {
	q, _ := newTestInteractive(Base{}, "time.Hour * 24")
	value, err := q.askDefault(nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if value.Source != "time.Hour * 24" {
		t.Fatalf("Source = %q, want %q", value.Source, "time.Hour * 24")
	}
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_default_no_user_entry
func TestQuestioner_QuestionerDefaultNoUserEntry(t *testing.T) {
	q, out := newTestInteractive(Base{}, "")
	value, err := q.askDefault(nil, "time.Hour * 24")
	if err != nil {
		t.Fatal(err)
	}
	if value.Source != "time.Hour * 24" {
		t.Fatalf("Source = %q, want the offered default", value.Source)
	}
	assertContains(t, out.String(), "press Enter to take time.Hour * 24, or type another expression")
	assertContains(t, out.String(), "[default: time.Hour * 24] >>> ")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_no_user_entry
func TestQuestioner_QuestionerNoDefaultNoUserEntry(t *testing.T) {
	q, out := newTestInteractive(Base{}, "", "exit")
	_, err := q.askDefault(nil, "")
	assertExit(t, err, ErrQuit)
	assertContains(t, out.String(), "enter an expression, or 'exit' to give up")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_syntax_error
func TestQuestioner_QuestionerNoDefaultSyntaxError(t *testing.T) {
	q, out := newTestInteractive(Base{}, "bad code", "exit")
	_, err := q.askDefault(nil, "")
	assertExit(t, err, ErrQuit)
	// Django prints "syntax error: invalid syntax"; go/parser's message
	// differs but the error class prefix is the same.
	assertContains(t, out.String(), "syntax error: ")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_name_error
//
// Django's NameError ("name 'datetim' is not defined") is go/types'
// "undefined: datetim", which gormgate reports as a TypeError.
func TestQuestioner_QuestionerNoDefaultNameError(t *testing.T) {
	q, out := newTestInteractive(Base{}, "datetim", "exit")
	_, err := q.askDefault(nil, "")
	assertExit(t, err, ErrQuit)
	assertContains(t, out.String(), "type error: undefined: datetim")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_attribute_error
//
// Django's AttributeError ("module 'datetime' has no attribute 'dat'") is
// go/types' "undefined: time.Dat".
func TestQuestioner_QuestionerNoDefaultAttributeError(t *testing.T) {
	q, out := newTestInteractive(Base{}, "time.Dat", "exit")
	_, err := q.askDefault(nil, "")
	assertExit(t, err, ErrQuit)
	assertContains(t, out.String(), "type error: undefined: time.Dat")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_keyboard_interrupt
//
// Go has no KeyboardInterrupt in a bufio reader; the equivalent is the input
// ending (Ctrl-D / a closed pipe). gormgate returns ErrQuit where
// Django calls sys.exit(1); the command layer turns that into the same exit
// status.
func TestQuestioner_QuestionerNoDefaultKeyboardInterrupt(t *testing.T) {
	q, out := newTestInteractive(Base{})
	_, err := q.askDefault(nil, "")
	assertExit(t, err, ErrQuit)
	assertContains(t, out.String(), "cancelled\n")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_default_no_user_entry_boolean
func TestQuestioner_QuestionerNoDefaultNoUserEntryBoolean(t *testing.T) {
	q, out := newTestInteractive(Base{}, "", "n")
	value, err := q.booleanInput("Proceed?", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value {
		t.Fatal("expected false")
	}
	assertContains(t, out.String(), "Proceed? ")
	assertContains(t, out.String(), "answer yes or no: ")
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_default_no_user_entry_boolean
func TestQuestioner_QuestionerDefaultNoUserEntryBoolean(t *testing.T) {
	q, _ := newTestInteractive(Base{}, "")
	value, err := q.booleanInput("Proceed?", m.Ptr(true))
	if err != nil {
		t.Fatal(err)
	}
	if !value {
		t.Fatal("expected true")
	}
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_bad_user_choice
func TestQuestioner_QuestionerBadUserChoice(t *testing.T) {
	q, out := newTestInteractive(Base{}, "10", "garbage", "1")
	const question = "Make a choice:"
	value, err := q.choiceInput(question, []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), question+"\n 1) a\n 2) b\n 3) c\n")
	assertContains(t, out.String(), "choose one of the numbers above: ")
	if value != 1 {
		t.Fatalf("choiceInput = %d, want 1", value)
	}
}

// django: tests/migrations/test_questioner.py QuestionerHelperMethodsTests.test_questioner_no_choice_keyboard_interrupt
//
// As above, the Go equivalent of KeyboardInterrupt is the input ending.
func TestQuestioner_QuestionerNoChoiceKeyboardInterrupt(t *testing.T) {
	q, out := newTestInteractive(Base{})
	const question = "Make a choice:"
	_, err := q.choiceInput(question, []string{"a", "b", "c"})
	assertExit(t, err, ErrQuit)
	assertContains(t, out.String(), question+"\n 1) a\n 2) b\n 3) c\nchoose: \ncancelled\n")
}

// ---------------------------------------------------------------------------
// The prompts themselves (Django exercises these through the autodetector and
// makemigrations tests).
// ---------------------------------------------------------------------------

// django: db/migrations/questioner.py InteractiveMigrationQuestioner.ask_not_null_addition
func TestQuestioner_InteractiveAskNotNullAddition(t *testing.T) {
	t.Run("one_off_default", func(t *testing.T) {
		q, out := newTestInteractive(Base{}, "1", "42")
		q.FieldLookup = func(model, field string) *m.Field { return &m.Field{Type: m.Int, Size: 64} }
		value, err := q.AskNotNullAddition("age", "Author")
		if err != nil {
			t.Fatal(err)
		}
		if value.Source != "42" {
			t.Fatalf("value = %#v, want GoExpr{42}", value)
		}
		assertContains(t, out.String(), "Author.age is not nullable and the rows already there need a value for it.")
	})
	t.Run("quit", func(t *testing.T) {
		q, _ := newTestInteractive(Base{}, "2")
		_, err := q.AskNotNullAddition("age", "Author")
		assertExit(t, err, ErrNoAnswer)
	})
	t.Run("dry_run", func(t *testing.T) {
		q, out := newTestInteractive(Base{DryRun: true})
		value, err := q.AskNotNullAddition("age", "Author")
		if value != nil || err != nil {
			t.Fatalf("dry run = %v, %v; want nil, nil", value, err)
		}
		if out.Len() != 0 {
			t.Fatalf("dry run should not prompt, got %q", out.String())
		}
	})
	t.Run("type_error_reprompts", func(t *testing.T) {
		// The entered default must fit the field being added.
		q, out := newTestInteractive(Base{}, "1", "42", `"forty-two"`)
		q.FieldLookup = func(model, field string) *m.Field { return &m.Field{Type: m.String, Size: 10} }
		value, err := q.AskNotNullAddition("name", "Author")
		if err != nil {
			t.Fatal(err)
		}
		if value.Source != `"forty-two"` {
			t.Fatalf("value = %#v, want GoExpr{\"forty-two\"}", value)
		}
		assertContains(t, out.String(), "type error: ")
	})
}

// django: db/migrations/questioner.py InteractiveMigrationQuestioner.ask_not_null_alteration
func TestQuestioner_InteractiveAskNotNullAlteration(t *testing.T) {
	t.Run("quit", func(t *testing.T) {
		q, _ := newTestInteractive(Base{}, "3")
		_, err := q.AskNotNullAlteration("age", "Author")
		assertExit(t, err, ErrNoAnswer)
	})
	t.Run("dry_run", func(t *testing.T) {
		q, _ := newTestInteractive(Base{DryRun: true})
		value, err := q.AskNotNullAlteration("age", "Author")
		if value != nil || err != nil {
			t.Fatalf("dry run = %v, %v; want nil, nil", value, err)
		}
	})
}

// django: db/migrations/questioner.py InteractiveMigrationQuestioner.ask_auto_now_add_addition
//
// Django offers "timezone.now" as the default; gormgate offers "time.Now",
// which CheckExpr accepts as a callable default for a time.Time field.
func TestQuestioner_InteractiveAskAutoNowAddAddition(t *testing.T) {
	t.Run("accepts_offered_default", func(t *testing.T) {
		q, out := newTestInteractive(Base{}, "1", "")
		q.FieldLookup = func(model, field string) *m.Field { return &m.Field{Type: m.Time} }
		value, err := q.AskAutoNowAddAddition("created", "Author")
		if err != nil {
			t.Fatal(err)
		}
		if value.Source != "time.Now" {
			t.Fatalf("value = %#v, want GoExpr{time.Now}", value)
		}
		assertContains(t, out.String(), "Author.created has autoCreateTime and the rows already there need a value for it.")
		assertContains(t, out.String(), "[default: time.Now] >>> ")
	})
	t.Run("quit", func(t *testing.T) {
		q, _ := newTestInteractive(Base{}, "2")
		_, err := q.AskAutoNowAddAddition("created", "Author")
		assertExit(t, err, ErrNoAnswer)
	})
}

// django: db/migrations/questioner.py InteractiveMigrationQuestioner.ask_unique_callable_default_addition
func TestQuestioner_InteractiveAskUniqueCallableDefaultAddition(t *testing.T) {
	t.Run("continue", func(t *testing.T) {
		q, out := newTestInteractive(Base{}, "1")
		if err := q.AskUniqueCallableDefaultAddition("token", "Author"); err != nil {
			t.Fatal(err)
		}
		assertContains(t, out.String(), "Author.token is unique and its default is a function")
	})
	t.Run("quit", func(t *testing.T) {
		q, _ := newTestInteractive(Base{}, "2")
		assertExit(t, q.AskUniqueCallableDefaultAddition("token", "Author"), ErrNoAnswer)
	})
}

// django: db/migrations/questioner.py InteractiveMigrationQuestioner.ask_rename / ask_rename_model / ask_merge
func TestQuestioner_InteractiveAskRename(t *testing.T) {
	q, out := newTestInteractive(Base{}, "y")
	ok, err := q.AskRename("Author", "name", "title", m.Field{Type: m.String, Size: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected yes")
	}
	assertContains(t, out.String(), "Was Author.name renamed to Author.title (a CharField)? [y/N]")

	q2, out2 := newTestInteractive(Base{}, "")
	ok2, err := q2.AskRenameModel(&m.ModelState{App: "blog", Name: "Author"}, &m.ModelState{App: "blog", Name: "Writer"})
	if err != nil {
		t.Fatal(err)
	}
	if ok2 {
		t.Fatal("empty answer must take the [y/N] default, which is no")
	}
	assertContains(t, out2.String(), "Was the model blog.Author renamed to Writer? [y/N]")

	q3, out3 := newTestInteractive(Base{}, "y")
	ok3, err := q3.AskMerge("blog")
	if err != nil {
		t.Fatal(err)
	}
	if !ok3 {
		t.Fatal("expected yes")
	}
	assertContains(t, out3.String(), "Should these migration branches be merged? [y/N]")
}

// ---------------------------------------------------------------------------
// NonInteractiveMigrationQuestioner
// ---------------------------------------------------------------------------

// django: db/migrations/questioner.py NonInteractiveMigrationQuestioner.ask_not_null_addition
func TestQuestioner_NonInteractiveAskNotNullAddition(t *testing.T) {
	var logged []string
	q := &NonInteractive{Verbosity: 1, Log: func(s string) { logged = append(logged, s) }}
	_, err := q.AskNotNullAddition("age", "Author")
	assertExit(t, err, ErrNoAnswer)
	want := "Author.age not migrated: it is impossible to add a non-nullable field without specifying a default"
	if len(logged) != 1 || logged[0] != want {
		t.Fatalf("log = %q, want [%q]", logged, want)
	}
}

// django: db/migrations/questioner.py NonInteractiveMigrationQuestioner.log_lack_of_migration
func TestQuestioner_NonInteractiveSilentWhenVerbosityZero(t *testing.T) {
	// verbosity 0 logs nothing (and so must not need a logger at all).
	q := &NonInteractive{Verbosity: 0}
	_, err := q.AskNotNullAddition("age", "Author")
	assertExit(t, err, ErrNoAnswer)
	_, err = q.AskAutoNowAddAddition("created", "Author")
	assertExit(t, err, ErrNoAnswer)
}

// django: db/migrations/questioner.py NonInteractiveMigrationQuestioner.ask_not_null_alteration
func TestQuestioner_NonInteractiveAskNotNullAlteration(t *testing.T) {
	var logged []string
	q := &NonInteractive{Verbosity: 1, Log: func(s string) { logged = append(logged, s) }}
	value, err := q.AskNotNullAlteration("age", "Author")
	if err != nil {
		t.Fatal(err)
	}
	if value != NotProvided {
		t.Fatalf("value = %v, want NotProvided", value)
	}
	want := "Field 'age' on model 'Author' given a default of NOT PROVIDED and must be corrected."
	if len(logged) != 1 || logged[0] != want {
		t.Fatalf("log = %q, want [%q]", logged, want)
	}
}

// django: db/migrations/questioner.py NonInteractiveMigrationQuestioner.ask_auto_now_add_addition
func TestQuestioner_NonInteractiveAskAutoNowAddAddition(t *testing.T) {
	var logged []string
	q := &NonInteractive{Verbosity: 1, Log: func(s string) { logged = append(logged, s) }}
	_, err := q.AskAutoNowAddAddition("created", "Author")
	assertExit(t, err, ErrNoAnswer)
	want := "Author.created not migrated: it is impossible to add a field with 'autoCreateTime' without specifying a default"
	if len(logged) != 1 || logged[0] != want {
		t.Fatalf("log = %q, want [%q]", logged, want)
	}
}

// ---------------------------------------------------------------------------
// CheckExpr (gormgate's replacement for Django's eval())
// ---------------------------------------------------------------------------

// django: db/migrations/questioner.py InteractiveMigrationQuestioner._ask_default
// (the eval() call). gormgate type-checks the Go expression against the field
// being added instead.
func TestQuestioner_CheckExpr(t *testing.T) {
	tests := []struct {
		name    string
		code    string
		field   *m.Field
		wantErr string // "" means accepted
	}{
		{name: "untyped_int_no_field", code: "42"},
		{name: "expression_no_field", code: "time.Hour * 24"},
		{name: "syntax_error", code: "bad code", wantErr: "syntax error: "},
		{name: "undefined_identifier", code: "datetim", wantErr: "type error: undefined: datetim"},
		{name: "undefined_selector", code: "time.Dat", wantErr: "type error: undefined: time.Dat"},
		{name: "string_for_string_field", code: `"x"`, field: &m.Field{Type: m.String, Size: 10}},
		{name: "int_for_string_field", code: "42", field: &m.Field{Type: m.String, Size: 10}, wantErr: "type error: "},
		{name: "callable_for_time_field", code: "time.Now", field: &m.Field{Type: m.Time}},
		{name: "value_for_time_field", code: "time.Time{}", field: &m.Field{Type: m.Time}},
		{name: "bool_for_bool_field", code: "true", field: &m.Field{Type: m.Bool}},
		{name: "float_for_float_field", code: "1.5", field: &m.Field{Type: m.Float}},
		{name: "bytes_for_bytes_field", code: `[]byte("x")`, field: &m.Field{Type: m.Bytes}},
		{
			// A custom Go type can't be named without importing the model's
			// package, so the expression is not checked against it.
			name: "custom_type_has_no_target_type", code: "42",
			field: &m.Field{Custom: &m.TypeRef{Package: "example.com/x", Name: "T"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckExpr(tt.code, tt.field)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("CheckExpr(%q) = %v, want accepted", tt.code, err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("CheckExpr(%q) = nil, want %q", tt.code, tt.wantErr)
			case tt.wantErr != "" && !strings.HasPrefix(err.Error(), tt.wantErr):
				t.Fatalf("CheckExpr(%q) = %q, want prefix %q", tt.code, err, tt.wantErr)
			}
		})
	}
}

// failingImporter stands in for the "source" importer on a machine with no
// Go toolchain on PATH, where it shells out to `go list` and fails.
type failingImporter struct{ calls int }

func (f *failingImporter) Import(path string) (*types.Package, error) {
	f.calls++
	return nil, fmt.Errorf("cannot find import %q", path)
}

// TestCheckExprNeedsNoGoToolchain checks the default prompt against a
// machine with no Go toolchain: importing the time package unconditionally
// made every answer — including "x" for a string field — fail with
// `type error: could not import time`, and left the prompt looping with no
// way out but typing "exit".
func TestCheckExprNeedsNoGoToolchain(t *testing.T) {
	imp := &failingImporter{}
	saved := newImporter
	newImporter = func(*token.FileSet) types.Importer { return imp }
	t.Cleanup(func() { newImporter = saved })

	// An expression that does not name the time package is checked with
	// no import at all.
	if err := CheckExpr(`"x"`, &m.Field{Type: m.String, Size: 10}); err != nil {
		t.Fatalf(`CheckExpr("x") = %v, want nil`, err)
	}
	if err := CheckExpr("42", &m.Field{Type: m.Int, Size: 64}); err != nil {
		t.Fatalf("CheckExpr(42) = %v, want nil", err)
	}
	if imp.calls != 0 {
		t.Fatalf("the importer ran %d times for expressions that need no import", imp.calls)
	}

	// It is still type-checked.
	if err := CheckExpr("42", &m.Field{Type: m.String, Size: 10}); err == nil {
		t.Fatal("CheckExpr(42) for a string field must report a type error")
	}
	// A syntax error is still a syntax error.
	if err := CheckExpr("1 +", &m.Field{Type: m.Int, Size: 64}); err == nil {
		t.Fatal("CheckExpr(\"1 +\") must report a syntax error")
	}

	// An expression that does need the time package is accepted rather
	// than blamed on the user when the importer cannot run.
	if err := CheckExpr("time.Now", &m.Field{Type: m.Time}); err != nil {
		t.Fatalf("CheckExpr(time.Now) = %v, want nil", err)
	}
	if imp.calls == 0 {
		t.Fatal("the importer did not run for an expression naming time")
	}
}
