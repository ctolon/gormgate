package gormgate

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

// errNoDatabase is what the test settings' Open returns: these tests drive
// commands that must resolve their settings before they touch a database,
// and reaching it is itself the signal that the app label was accepted.
var errNoDatabase = errors.New("no database in this test")

// settingsSet builds two named settings that differ only in the app they
// declare, so that a command's output says which one it ran with.
func settingsSet() SettingsSet {
	return SettingsSet{
		"a": {
			Apps:      []*AppConfig{App("aapp")},
			Databases: map[string]Database{"default": {Open: func(context.Context) (*gorm.DB, error) { return nil, errNoDatabase }}},
			Command:   "go run ./cmd/gorm-gate",
		},
		"b": {
			Apps:      []*AppConfig{App("bapp")},
			Databases: map[string]Database{"default": {Open: func(context.Context) (*gorm.DB, error) { return nil, errNoDatabase }}},
			Command:   "go run ./cmd/gorm-gate",
		},
	}
}

// TestSettingsSelection checks the precedence of --settings, the
// GORMGATE_SETTINGS_MODULE environment variable and the name passed to
// ExecuteSet. The command is makemigrations "aapp": the settings that
// declare that app accept the label and go on to open the database, while
// the other settings reject the label before ever reaching it, so the two
// outcomes say which settings were selected.
func TestSettingsSelection(t *testing.T) {
	env := func(pairs ...string) func(string) (string, bool) {
		return func(k string) (string, bool) {
			for i := 0; i < len(pairs); i += 2 {
				if pairs[i] == k {
					return pairs[i+1], true
				}
			}
			return "", false
		}
	}
	const (
		selectedA = "no database in this test"
		selectedB = "No installed app with label 'aapp'."
	)
	for _, tc := range []struct {
		name   string
		argv   []string
		getenv func(string) (string, bool)
		def    string
		want   string
	}{
		{name: "default name", argv: []string{"gorm-gate", "makemigrations", "aapp"}, def: "a", want: selectedA},
		{name: "environment wins over the default name", argv: []string{"gorm-gate", "makemigrations", "aapp"},
			getenv: env("GORMGATE_SETTINGS_MODULE", "b"), def: "a", want: selectedB},
		{name: "empty environment value keeps the default name", argv: []string{"gorm-gate", "makemigrations", "aapp"},
			getenv: env("GORMGATE_SETTINGS_MODULE", ""), def: "a", want: selectedA},
		{name: "--settings wins over both", argv: []string{"gorm-gate", "makemigrations", "aapp", "--settings", "b"},
			getenv: env("GORMGATE_SETTINGS_MODULE", "a"), def: "a", want: selectedB},
		{name: "unknown name is reported with the available ones",
			argv: []string{"gorm-gate", "makemigrations", "aapp", "--settings", "zzz"},
			def:  "a", want: `settings "zzz" not found (available: a, b)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			streams := &IO{Stdout: &out, Stderr: &out, Getenv: tc.getenv}
			ExecuteArgs(context.Background(), settingsSet(), tc.def, tc.argv, streams)
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("output does not contain %q:\n%s", tc.want, out.String())
			}
		})
	}
}
