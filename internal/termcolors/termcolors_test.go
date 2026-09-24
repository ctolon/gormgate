package termcolors

import (
	"testing"
)

// env builds the Env a test runs against: a terminal by default, with the
// given variables set.
func env(tty bool, vars map[string]string) Env {
	return Env{
		Getenv: func(k string) (string, bool) {
			v, ok := vars[k]
			return v, ok
		},
		StdoutIsTTY: func() bool { return tty },
	}
}

// The expectations below were taken from Django 6.0.8 itself, by calling
// django.utils.termcolors.parse_color_setting and colorize on the same
// settings.
//
// django: tests/utils_tests/test_termcolors.py
func TestColorStyle_Palettes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting string
		// want maps a styled sample to the expected output of applying
		// the role to "x".
		want map[string]string
	}{
		{
			name:    "default is the dark palette",
			setting: "",
			want: map[string]string{
				"MIGRATE_HEADING": "\x1b[36;1mx\x1b[0m",
				"MIGRATE_LABEL":   "\x1b[1mx\x1b[0m",
				"SQL_KEYWORD":     "\x1b[33mx\x1b[0m",
				"ERROR":           "\x1b[31;1mx\x1b[0m",
			},
		},
		{
			name:    "nocolor leaves every role alone",
			setting: "nocolor",
			want:    map[string]string{"ERROR": "x", "SQL_KEYWORD": "x"},
		},
		{
			name:    "the light palette differs in SQL_KEYWORD",
			setting: "light",
			want:    map[string]string{"SQL_KEYWORD": "\x1b[34mx\x1b[0m"},
		},
		{
			name:    "a palette can be overridden role by role",
			setting: "light;error=blue",
			want: map[string]string{
				"ERROR":       "\x1b[34mx\x1b[0m",
				"SQL_KEYWORD": "\x1b[34mx\x1b[0m",
			},
		},
		{
			name:    "two colors are foreground and background",
			setting: "migrate_label=red/blue",
			want:    map[string]string{"MIGRATE_LABEL": "\x1b[31;44mx\x1b[0m"},
		},
		{
			// Django reverses the list and pops the foreground off the
			// end, so the background is the second color named, not the
			// last one.
			name:    "a third color is ignored, not preferred",
			setting: "migrate_label=red/blue/green",
			want:    map[string]string{"MIGRATE_LABEL": "\x1b[31;44mx\x1b[0m"},
		},
		{
			name:    "an unknown background is dropped",
			setting: "migrate_label=green/notacolor",
			want:    map[string]string{"MIGRATE_LABEL": "\x1b[32mx\x1b[0m"},
		},
		{
			name:    "options are applied in reverse order",
			setting: "error=red,bold,blink",
			want:    map[string]string{"ERROR": "\x1b[31;5;1mx\x1b[0m"},
		},
		{
			// "noreset" is not in Django's opt_dict, so the palette
			// syntax cannot select it.
			name:    "noreset is not a palette option",
			setting: "sql_keyword=yellow,noreset",
			want:    map[string]string{"SQL_KEYWORD": "\x1b[33mx\x1b[0m"},
		},
		{
			name:    "an empty definition selects nothing at all",
			setting: "migrate_label=",
			want:    map[string]string{"MIGRATE_LABEL": "x", "ERROR": "x"},
		},
		{
			name:    "an unknown role is ignored",
			setting: "bogus=red",
			want:    map[string]string{"ERROR": "x"},
		},
		{
			// Django's "role, instructions = part.split('=')" raises here
			// and the command dies; gormgate ignores the part
			// (docs/deviations.md).
			name:    "a definition with a second = is ignored",
			setting: "migrate_label=a=b",
			want:    map[string]string{"MIGRATE_LABEL": "x"},
		},
		{
			name:    "nocolor after a palette is a no-op, as in Django",
			setting: "light;nocolor",
			want:    map[string]string{"SQL_KEYWORD": "\x1b[34mx\x1b[0m"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ColorStyle(true, env(false, map[string]string{"GORMGATE_COLORS": tc.setting}))
			apply := map[string]func(string) string{
				"ERROR": s.Error, "SUCCESS": s.Success, "WARNING": s.Warning,
				"NOTICE": s.Notice, "SQL_FIELD": s.SQLField, "SQL_COLTYPE": s.SQLColtype,
				"SQL_KEYWORD": s.SQLKeyword, "SQL_TABLE": s.SQLTable,
				"MIGRATE_HEADING": s.MigrateHeading, "MIGRATE_LABEL": s.MigrateLabel,
			}
			for role, want := range tc.want {
				f, ok := apply[role]
				if !ok {
					t.Fatalf("no Style method for role %s", role)
				}
				if got := f("x"); got != want {
					t.Errorf("%s(%q) = %q, want %q", role, "x", got, want)
				}
			}
		})
	}
}

func TestColorStyle_SettingSource(t *testing.T) {
	blue := "\x1b[34mx\x1b[0m"
	yellow := "\x1b[33mx\x1b[0m"
	for _, tc := range []struct {
		name string
		vars map[string]string
		want string
	}{
		{"GORMGATE_COLORS is read", map[string]string{"GORMGATE_COLORS": "light"}, blue},
		{"DJANGO_COLORS is honoured", map[string]string{"DJANGO_COLORS": "light"}, blue},
		{
			"GORMGATE_COLORS wins",
			map[string]string{"GORMGATE_COLORS": "dark", "DJANGO_COLORS": "light"},
			yellow,
		},
		{
			// Set but empty shadows DJANGO_COLORS and selects the default
			// palette, which is Django's behaviour for an empty setting.
			"an empty GORMGATE_COLORS shadows DJANGO_COLORS",
			map[string]string{"GORMGATE_COLORS": "", "DJANGO_COLORS": "light"},
			yellow,
		},
		{"neither is set", nil, yellow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := ColorStyle(true, env(false, tc.vars))
			if got := s.SQLKeyword("x"); got != tc.want {
				t.Errorf("SQLKeyword(%q) = %q, want %q", "x", got, tc.want)
			}
		})
	}
}

func TestColorStyle_TerminalDetection(t *testing.T) {
	vars := map[string]string{"GORMGATE_COLORS": "light"}
	if got := ColorStyle(false, env(false, vars)).SQLKeyword("x"); got != "x" {
		t.Errorf("without a terminal and without --force-color: %q, want %q", got, "x")
	}
	// On Windows a terminal only takes colour when one of the markers
	// supportsColor looks for is set, so this case sets one: without it the
	// assertion would be about the platform rather than about the style.
	onTTY := map[string]string{"GORMGATE_COLORS": "light", "WT_SESSION": "1"}
	if got := ColorStyle(false, env(true, onTTY)).SQLKeyword("x"); got != "\x1b[34mx\x1b[0m" {
		t.Errorf("on a terminal: %q, want the light SQL_KEYWORD", got)
	}
	if got := ColorStyle(true, env(false, vars)).SQLKeyword("x"); got != "\x1b[34mx\x1b[0m" {
		t.Errorf("with --force-color: %q, want the light SQL_KEYWORD", got)
	}
	if got := NoStyle().SQLKeyword("x"); got != "x" {
		t.Errorf("NoStyle: %q, want %q", got, "x")
	}
}
