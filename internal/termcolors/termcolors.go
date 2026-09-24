// Package termcolors styles terminal output the way Django does: it parses
// the DJANGO_COLORS palette syntax (read from GORMGATE_COLORS, falling back
// to DJANGO_COLORS) and wraps text in the resulting ANSI graphics codes.
//
// django: utils/termcolors.py, core/management/color.py
package termcolors

import (
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
)

// colorNames are the eight ANSI colors, in code order: index i is the
// foreground code 3i and the background code 4i.
var colorNames = []string{"black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"}

// optionCodes maps a display option to its ANSI code. "noreset" is
// accepted by colorize but has no code of its own: it suppresses the
// trailing reset.
var optionCodes = map[string]string{
	"bold":       "1",
	"underscore": "4",
	"blink":      "5",
	"reverse":    "7",
	"conceal":    "8",
}

// colorIndex returns the ANSI index of a color name.
func colorIndex(name string) (int, bool) {
	i := slices.Index(colorNames, name)
	return i, i >= 0
}

// definition is one role's style: foreground color, background color and
// display options. The zero definition still colorizes, emitting an empty
// graphics code followed by a reset, exactly as Django's does.
type definition struct {
	fg, bg string
	opts   []string
}

// empty reports whether the definition selects nothing at all.
func (d definition) empty() bool { return d.fg == "" && d.bg == "" && len(d.opts) == 0 }

// colorize encloses text in the ANSI graphics codes d selects.
//
// django: utils/termcolors.py colorize
func colorize(text string, d definition) string {
	if text == "" && len(d.opts) == 1 && d.opts[0] == "reset" {
		return "\x1b[0m"
	}
	var codes []string
	if i, ok := colorIndex(d.fg); ok {
		codes = append(codes, strconv.Itoa(30+i))
	}
	if i, ok := colorIndex(d.bg); ok {
		codes = append(codes, strconv.Itoa(40+i))
	}
	noreset := false
	for _, o := range d.opts {
		if c, ok := optionCodes[o]; ok {
			codes = append(codes, c)
		}
		if o == "noreset" {
			noreset = true
		}
	}
	if !noreset {
		text = text + "\x1b[0m"
	}
	return "\x1b[" + strings.Join(codes, ";") + "m" + text
}

// A role names one kind of styled output, such as an error message or an SQL
// keyword. Roles are spelled in upper case because that is how they appear in
// a DJANGO_COLORS setting.
type role string

// The roles of Django's nocolor palette, which is the complete set.
const (
	roleError           role = "ERROR"
	roleSuccess         role = "SUCCESS"
	roleWarning         role = "WARNING"
	roleNotice          role = "NOTICE"
	roleSQLField        role = "SQL_FIELD"
	roleSQLColtype      role = "SQL_COLTYPE"
	roleSQLKeyword      role = "SQL_KEYWORD"
	roleSQLTable        role = "SQL_TABLE"
	roleHTTPInfo        role = "HTTP_INFO"
	roleHTTPSuccess     role = "HTTP_SUCCESS"
	roleHTTPRedirect    role = "HTTP_REDIRECT"
	roleHTTPNotModified role = "HTTP_NOT_MODIFIED"
	roleHTTPBadRequest  role = "HTTP_BAD_REQUEST"
	roleHTTPNotFound    role = "HTTP_NOT_FOUND"
	roleHTTPServerError role = "HTTP_SERVER_ERROR"
	roleMigrateHeading  role = "MIGRATE_HEADING"
	roleMigrateLabel    role = "MIGRATE_LABEL"
)

// roles is every role a palette may define. Django derives the same set from
// its nocolor palette, which maps each role to an empty definition.
var roles = []role{
	roleError, roleSuccess, roleWarning, roleNotice,
	roleSQLField, roleSQLColtype, roleSQLKeyword, roleSQLTable,
	roleHTTPInfo, roleHTTPSuccess, roleHTTPRedirect, roleHTTPNotModified,
	roleHTTPBadRequest, roleHTTPNotFound, roleHTTPServerError,
	roleMigrateHeading, roleMigrateLabel,
}

func isRole(r role) bool { return slices.Contains(roles, r) }

// A palette gives a definition to some or all roles. A role it does not
// mention is styled by the zero definition.
type palette map[role]definition

// d is the shorthand the palette tables below are written with.
func d(fg string, o ...string) definition { return definition{fg: fg, opts: o} }

// palettes are Django's named palettes, selectable by name in a color
// setting.
var palettes = map[string]palette{
	"nocolor": {},
	"dark": {
		roleError: d("red", "bold"), roleSuccess: d("green", "bold"), roleWarning: d("yellow", "bold"),
		roleNotice: d("red"), roleSQLField: d("green", "bold"), roleSQLColtype: d("green"),
		roleSQLKeyword: d("yellow"), roleSQLTable: d("", "bold"), roleHTTPInfo: d("", "bold"),
		roleHTTPSuccess: {}, roleHTTPRedirect: d("green"), roleHTTPNotModified: d("cyan"),
		roleHTTPBadRequest: d("red", "bold"), roleHTTPNotFound: d("yellow"),
		roleHTTPServerError: d("magenta", "bold"), roleMigrateHeading: d("cyan", "bold"),
		roleMigrateLabel: d("", "bold"),
	},
	"light": {
		roleError: d("red", "bold"), roleSuccess: d("green", "bold"), roleWarning: d("yellow", "bold"),
		roleNotice: d("red"), roleSQLField: d("green", "bold"), roleSQLColtype: d("green"),
		roleSQLKeyword: d("blue"), roleSQLTable: d("", "bold"), roleHTTPInfo: d("", "bold"),
		roleHTTPSuccess: {}, roleHTTPRedirect: d("green", "bold"), roleHTTPNotModified: d("green"),
		roleHTTPBadRequest: d("red", "bold"), roleHTTPNotFound: d("red"),
		roleHTTPServerError: d("magenta", "bold"), roleMigrateHeading: d("cyan", "bold"),
		roleMigrateLabel: d("", "bold"),
	},
}

// parseColorSetting parses the value of a GORMGATE_COLORS or DJANGO_COLORS
// setting. A nil result means the setting asks for no colors at all; the
// empty string selects the default dark palette.
//
// The syntax is a ";"-separated list of either a palette name or a
// "role=fg/bg,opt,opt" definition.
//
// django: utils/termcolors.py parse_color_setting
func parseColorSetting(config string) palette {
	if config == "" {
		return palettes["dark"]
	}
	p := palette{}
	for _, part := range strings.Split(strings.ToLower(config), ";") {
		if named, ok := palettes[part]; ok {
			for k, v := range named {
				p[k] = v
			}
			continue
		}
		name, instructions, ok := strings.Cut(part, "=")
		if !ok || strings.Contains(instructions, "=") {
			// Django's "role, instructions = part.split(\"=\")" raises a
			// ValueError on a second "="; gormgate ignores the part
			// instead (docs/deviations.md).
			continue
		}
		r := role(strings.ToUpper(name))
		styles := strings.Split(instructions, ",")
		colors := strings.Split(styles[0], "/")
		var def definition
		if _, ok := colorIndex(colors[0]); ok {
			def.fg = colors[0]
		}
		// Django reverses the color list and pops the foreground, so the
		// background it then reads off the end is the *second* color a
		// "fg/bg/..." definition names, not the last one.
		if len(colors) > 1 {
			if _, ok := colorIndex(colors[1]); ok {
				def.bg = colors[1]
			}
		}
		// Django reverses the instruction list before collecting the
		// options, so they end up in reverse order; keep that order.
		for i := len(styles) - 1; i >= 1; i-- {
			if _, ok := optionCodes[styles[i]]; ok {
				def.opts = append(def.opts, styles[i])
			}
		}
		if isRole(r) && !def.empty() {
			p[r] = def
		}
	}
	for _, def := range p {
		if !def.empty() {
			return p
		}
	}
	return nil
}

// A Style styles a string according to its role. The zero Style is not
// usable; build one with ColorStyle or NoStyle.
type Style struct {
	// palette is nil when styling is off, in which case every role
	// returns its argument unchanged.
	palette palette
}

func (s *Style) apply(r role, text string) string {
	if s.palette == nil {
		return text
	}
	return colorize(text, s.palette[r])
}

// Error styles an error message.
func (s *Style) Error(t string) string { return s.apply(roleError, t) }

// Success styles a message reporting that something worked.
func (s *Style) Success(t string) string { return s.apply(roleSuccess, t) }

// Warning styles a warning.
func (s *Style) Warning(t string) string { return s.apply(roleWarning, t) }

// Notice styles an advisory note.
func (s *Style) Notice(t string) string { return s.apply(roleNotice, t) }

// SQLField styles a column name in emitted SQL.
func (s *Style) SQLField(t string) string { return s.apply(roleSQLField, t) }

// SQLColtype styles a column type in emitted SQL.
func (s *Style) SQLColtype(t string) string { return s.apply(roleSQLColtype, t) }

// SQLKeyword styles a keyword in emitted SQL.
func (s *Style) SQLKeyword(t string) string { return s.apply(roleSQLKeyword, t) }

// SQLTable styles a table name in emitted SQL.
func (s *Style) SQLTable(t string) string { return s.apply(roleSQLTable, t) }

// MigrateHeading styles a heading printed by the migration commands.
func (s *Style) MigrateHeading(t string) string { return s.apply(roleMigrateHeading, t) }

// MigrateLabel styles a label printed by the migration commands.
func (s *Style) MigrateLabel(t string) string { return s.apply(roleMigrateLabel, t) }

// makeStyle builds a Style from a color setting.
//
// django: core/management/color.py make_style
func makeStyle(config string) *Style { return &Style{palette: parseColorSetting(config)} }

// NoStyle returns a Style that leaves every string unchanged.
func NoStyle() *Style { return makeStyle("nocolor") }

// Env is the environment ColorStyle consults. A nil field falls back to the
// process environment; tests set them to supply their own.
type Env struct {
	// Getenv looks up an environment variable, reporting whether it is set.
	Getenv func(string) (string, bool)
	// StdoutIsTTY reports whether standard output is a terminal.
	StdoutIsTTY func() bool
}

func (e Env) lookup(k string) (string, bool) {
	if e.Getenv != nil {
		return e.Getenv(k)
	}
	return os.LookupEnv(k)
}

// supportsColor reports whether standard output is a terminal that
// understands ANSI color codes.
//
// django: core/management/color.py supports_color
func supportsColor(e Env) bool {
	if e.StdoutIsTTY == nil || !e.StdoutIsTTY() {
		return false
	}
	if runtime.GOOS != "windows" {
		return true
	}
	if _, ok := e.lookup("ANSICON"); ok {
		return true
	}
	if _, ok := e.lookup("WT_SESSION"); ok {
		return true
	}
	v, _ := e.lookup("TERM_PROGRAM")
	return v == "vscode"
}

// colorsSetting reads GORMGATE_COLORS, falling back to DJANGO_COLORS.
func colorsSetting(e Env) string {
	if v, ok := e.lookup("GORMGATE_COLORS"); ok {
		return v
	}
	v, _ := e.lookup("DJANGO_COLORS")
	return v
}

// ColorStyle returns the Style to use for terminal output. Unless forceColor
// is set, it returns NoStyle when the terminal does not support color.
//
// django: core/management/color.py color_style
func ColorStyle(forceColor bool, e Env) *Style {
	if !forceColor && !supportsColor(e) {
		return NoStyle()
	}
	return makeStyle(colorsSetting(e))
}
