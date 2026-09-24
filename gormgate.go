// Package gormgate provides Django-style migrations for gorm: migrations
// are Go files, and the management commands (makemigrations, migrate,
// sqlmigrate, showmigrations, squashmigrations, optimizemigration,
// sqlsequencereset, inspectdb) behave like Django's.
//
// A project runs them from its own gorm-gate command:
//
//	func main() {
//		gormgate.Execute(&gormgate.Settings{
//			Apps: []*gormgate.AppConfig{
//				gormgate.App("blog", &blog.Post{}, &blog.Comment{}),
//			},
//			Databases: map[string]gormgate.Database{
//				"default": {Open: func(ctx context.Context) (*gorm.DB, error) {
//					return gorm.Open(postgres.Open(dsn))
//				}},
//			},
//		})
//	}
package gormgate

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/management"
	"github.com/ctolon/gormgate/internal/writer"
	m "github.com/ctolon/gormgate/migrations"
)

// modulePath is this module's path, used to find its own version in the
// build information of whatever program linked it.
const modulePath = "github.com/ctolon/gormgate"

// Version returns the gormgate version, as "version" prints it. It is read
// from the build information of the running program, so it is whatever
// version of this module the program was actually built against and there
// is nothing to keep in step with a release tag. A program built inside
// this repository, or with a replace directive pointing at a checkout,
// reports "(devel)" -- there is no released version to name.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "(devel)"
	}
	if info.Main.Path == modulePath {
		return moduleVersion(&info.Main)
	}
	for _, dep := range info.Deps {
		if dep.Path == modulePath {
			return moduleVersion(dep)
		}
	}
	return "(devel)"
}

// moduleVersion turns a build-info module into the string a version command
// prints: "v0.1.0" becomes "0.1.0". A module that a replace directive
// points at a checkout has no released version to name, and neither does
// one built from the repository itself, so both report "(devel)".
func moduleVersion(mod *debug.Module) string {
	if mod.Replace != nil {
		if mod.Replace.Version == "" {
			return "(devel)"
		}
		mod = mod.Replace
	}
	if mod.Version == "" || mod.Version == "(devel)" {
		return "(devel)"
	}
	return strings.TrimPrefix(mod.Version, "v")
}

// The version is read from build information rather than written into a
// source file, so nothing has to be edited to cut a release. Both the
// places that report it are wired to the one function.
func init() {
	management.Version = Version
	writer.Version = Version()
}

// AppConfig is an installed app: a label and its models.
type AppConfig struct {
	Label  string
	Models []any

	dir        string
	importPath string
}

// App declares an app with its models (pointers to gorm model structs).
// By default its migrations live in the "migrations" package next to the
// models' package.
func App(label string, models ...any) *AppConfig {
	return &AppConfig{Label: label, Models: models}
}

// Migrations overrides where the app's migrations live: dir is relative to
// the module root (or absolute) and importPath is the package's import
// path.
func (a *AppConfig) Migrations(dir, importPath string) *AppConfig {
	a.dir, a.importPath = dir, importPath
	return a
}

// Database is one configured database.
type Database struct {
	// Open returns a gorm handle. It is called at most once per command,
	// the first time that database is used, with the context passed to
	// Execute, ExecuteArgs or CallCommand.
	Open func(ctx context.Context) (*gorm.DB, error)
}

// Router decides whether a model may be migrated on a database (Django's
// database routers).
type Router = base.Router

// Hints are what a Router is told about the migration it is asked about:
// the model, its lower-cased name, and whatever Hints map the operation
// itself was written with.
type Hints = m.Hints

// Hook runs before or after migrate for one app; it is told about the run
// through a MigrateRun (Django's pre_migrate / post_migrate signals).
type Hook = management.MigrateHook

// MigrateRun is what a Hook is told about the migrate run: the app, the
// connection, the plan, the verbosity and whether the command may prompt.
type MigrateRun = management.MigrateRun

// Settings configure a project.
type Settings struct {
	// Apps are the installed apps, in dependency order.
	Apps []*AppConfig
	// Databases must contain "default".
	Databases map[string]Database
	// NamingStrategy must equal the gorm.Config naming strategy of the
	// application.
	NamingStrategy schema.Namer
	// DisableForeignKeyConstraintWhenMigrating and
	// IgnoreRelationshipsWhenMigrating mirror gorm.Config.
	DisableForeignKeyConstraintWhenMigrating bool
	IgnoreRelationshipsWhenMigrating         bool
	// MigrationModules overrides (or, with a nil value, disables) the
	// migrations package of an app, like Django's MIGRATION_MODULES.
	MigrationModules map[string]*string
	Routers          []Router
	// Command is how the gorm-gate command is invoked; it replaces
	// "python manage.py" in messages (default "go run ./cmd/gorm-gate").
	Command string
	// CommandDir is the directory of the gorm-gate command's package, where
	// the generated file importing every migrations package is written.
	// It defaults to the directory of the main package.
	CommandDir string
	// CommandPackage is that package's name (default "main").
	CommandPackage string
	// SilencedChecks are system check IDs to ignore.
	SilencedChecks []string
	// PreMigrate and PostMigrate hooks run around migrate, per app label.
	PreMigrate  map[string][]Hook
	PostMigrate map[string][]Hook
}

// SettingsSet is a named set of settings, selected with --settings or the
// GORMGATE_SETTINGS_MODULE environment variable.
type SettingsSet map[string]*Settings

// Execute runs the command line in os.Args and exits with its status.
func Execute(s *Settings) {
	os.Exit(ExecuteArgs(context.Background(), SettingsSet{"default": s}, "default", os.Args, nil))
}

// ExecuteSet runs the command line with a named set of settings;
// defaultName (or GORMGATE_SETTINGS_MODULE) selects one.
func ExecuteSet(set SettingsSet, defaultName string) {
	os.Exit(ExecuteArgs(context.Background(), set, defaultName, os.Args, nil))
}

// IO replaces the streams and the process environment a command sees. It
// is how tests drive the commands; a nil field keeps the process value.
type IO struct {
	// Stdout, Stderr and Stdin replace the process streams.
	Stdout, Stderr io.Writer
	Stdin          io.Reader
	// StdoutIsTTY and StderrIsTTY report whether the streams are
	// terminals, which decides whether output is colored.
	StdoutIsTTY, StderrIsTTY func() bool
	// Getenv looks up an environment variable, reporting whether it is
	// set.
	Getenv func(string) (string, bool)
	// Getwd returns the working directory.
	Getwd func() (string, error)
}

// ExecuteArgs runs argv against a settings set and returns the exit status.
// streams, when non-nil, replaces the standard streams and environment.
//
// ctx is passed to Database.Open and attached to the session every
// statement runs on, so cancelling it cancels the migration in progress.
func ExecuteArgs(ctx context.Context, set SettingsSet, defaultName string, argv []string, streams *IO) int {
	name := settingsFor(defaultName, streams)
	env := buildEnv(streams)
	projects := map[string]func() (*management.Project, error){}
	for n, s := range set {
		projects[n] = func() (*management.Project, error) { return buildProject(ctx, s) }
	}
	prog := "gorm-gate"
	if len(argv) > 0 {
		prog = filepath.Base(argv[0])
	}
	return management.Execute(env, prog, projects, name, argv)
}

// Command returns the gorm-gate command tree for a settings set, so that a
// project can mount it inside its own cobra command instead of handing the
// whole program over to Execute. defaultName selects the settings when
// --settings is absent.
//
// The returned command opens the chosen project when it runs and closes it
// afterwards; the caller does not have to.
func Command(ctx context.Context, set SettingsSet, defaultName string, streams *IO) *cobra.Command {
	projects := map[string]func() (*management.Project, error){}
	for n, s := range set {
		projects[n] = func() (*management.Project, error) { return buildProject(ctx, s) }
	}
	return management.Root(buildEnv(streams), "gorm-gate", projects, settingsFor(defaultName, streams))
}

// CallCommand runs one command in-process instead of from the command line.
// streams, when non-nil, replaces the standard streams and environment the
// command sees, which is how a caller captures its output.
//
// ctx is passed to Database.Open and attached to the session every
// statement runs on, so cancelling it cancels the migration in progress.
//
// django: core/management/__init__.py call_command
func CallCommand(ctx context.Context, s *Settings, streams *IO, name string, args ...string) error {
	p, err := buildProject(ctx, s)
	if err != nil {
		return err
	}
	defer p.Close()
	return management.Call(p, buildEnv(streams), name, args...)
}

// buildEnv builds the environment a command runs in: the process streams,
// with each field override supplies replacing one of them. Replacing a
// stream also makes it report as not a terminal, unless the caller says
// otherwise, so captured output carries no ANSI codes.
func buildEnv(override *IO) *management.Env {
	env := &management.Env{
		Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin,
		StdoutIsTTY: func() bool { return isTerminal(os.Stdout) },
		StderrIsTTY: func() bool { return isTerminal(os.Stderr) },
		StdinIsTTY:  func() bool { return isTerminal(os.Stdin) },
		Getenv:      os.LookupEnv,
		Getwd:       os.Getwd,
	}
	if override == nil {
		return env
	}
	if override.Stdout != nil {
		env.Stdout = override.Stdout
		env.StdoutIsTTY = func() bool { return false }
	}
	if override.Stderr != nil {
		env.Stderr = override.Stderr
		env.StderrIsTTY = func() bool { return false }
	}
	if override.Stdin != nil {
		env.Stdin = override.Stdin
	}
	if override.StdoutIsTTY != nil {
		env.StdoutIsTTY = override.StdoutIsTTY
	}
	if override.StderrIsTTY != nil {
		env.StderrIsTTY = override.StderrIsTTY
	}
	if override.Getenv != nil {
		env.Getenv = override.Getenv
	}
	if override.Getwd != nil {
		env.Getwd = override.Getwd
	}
	return env
}

// settingsFor resolves which settings of the set the command line selects
// when --settings does not: GORMGATE_SETTINGS_MODULE, then the name the
// caller passed. --settings itself is an ordinary flag on the command
// tree and overrides this.
func settingsFor(name string, override *IO) string {
	lookup := os.LookupEnv
	if override != nil && override.Getenv != nil {
		lookup = override.Getenv
	}
	if v, ok := lookup("GORMGATE_SETTINGS_MODULE"); ok && v != "" {
		return v
	}
	return name
}

func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// buildProject turns Settings into the internal project.
func buildProject(ctx context.Context, s *Settings) (*management.Project, error) {
	if s == nil {
		return nil, fmt.Errorf("gormgate: no settings")
	}
	if _, ok := s.Databases["default"]; !ok {
		return nil, fmt.Errorf("gormgate: Settings.Databases must contain a \"default\" database")
	}
	root, modulePath, err := moduleInfo()
	if err != nil {
		return nil, err
	}
	p := &management.Project{
		Ctx:                                      ctx,
		Databases:                                map[string]func() (*gorm.DB, error){},
		Namer:                                    s.NamingStrategy,
		DisableForeignKeyConstraintWhenMigrating: s.DisableForeignKeyConstraintWhenMigrating,
		IgnoreRelationshipsWhenMigrating:         s.IgnoreRelationshipsWhenMigrating,
		Command:                                  s.Command,
		CommandDir:                               s.CommandDir,
		CommandPackage:                           s.CommandPackage,
		SilencedChecks:                           s.SilencedChecks,
	}
	p.Routers = append(p.Routers, s.Routers...)
	for alias, db := range s.Databases {
		open := db.Open
		p.Databases[alias] = func() (*gorm.DB, error) { return open(ctx) }
	}
	if p.Command == "" {
		p.Command = "go run ./cmd/gorm-gate"
	}
	if p.CommandDir == "" {
		p.CommandDir = mainPackageDir(root, modulePath)
	} else if !filepath.IsAbs(p.CommandDir) {
		p.CommandDir = filepath.Join(root, p.CommandDir)
	}
	for _, a := range s.Apps {
		app := &management.App{Label: a.Label, Models: a.Models}
		importPath, dir := a.importPath, a.dir
		if importPath == "" {
			// A migrations package compiled into this command knows its own
			// directory; it is the authoritative location. Otherwise the
			// package next to the app's models is used, and an app without
			// models is simply unmigrated until it gets some (Django treats
			// an app without a models module the same way).
			if regDir, ok := m.RegisteredPackageDir(a.Label); ok {
				dir = regDir
				if modulePath != "" {
					if rel, err := filepath.Rel(root, regDir); err == nil && !strings.HasPrefix(rel, "..") {
						importPath = path.Join(modulePath, filepath.ToSlash(rel))
					}
				}
			} else if pkg := modelsPackage(a.Models); pkg != "" {
				importPath = pkg + "/migrations"
			}
		}
		if override, ok := s.MigrationModules[a.Label]; ok {
			if override == nil {
				app.Disabled = true
			} else {
				importPath = *override
			}
		}
		if dir == "" && modulePath != "" && strings.HasPrefix(importPath, modulePath) {
			dir = filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(importPath, modulePath)))
		} else if dir != "" && !filepath.IsAbs(dir) {
			dir = filepath.Join(root, dir)
		}
		app.MigrationsImport, app.MigrationsDir = importPath, dir
		p.Apps = append(p.Apps, app)
	}
	p.PreMigrateHooks = maps.Clone(s.PreMigrate)
	p.PostMigrateHooks = maps.Clone(s.PostMigrate)
	return p, nil
}

// modelsPackage returns the import path of the package the models are
// declared in.
func modelsPackage(models []any) string {
	for _, model := range models {
		t := reflect.TypeOf(model)
		for t != nil && t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t != nil && t.PkgPath() != "" {
			return t.PkgPath()
		}
	}
	return ""
}

// moduleInfo finds the module root directory and path by walking up from
// the working directory.
func moduleInfo() (string, string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", err
	}
	for {
		gomod := filepath.Join(dir, "go.mod")
		if b, err := os.ReadFile(gomod); err == nil {
			return dir, modulePathOf(string(b)), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			// Not inside a module: commands that only touch the database
			// still work; the ones writing files report it.
			wd, _ := os.Getwd()
			return wd, "", nil
		}
		dir = parent
	}
}

func modulePathOf(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "module "); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}

// mainPackageDir maps the running main package to its source directory.
func mainPackageDir(root, modulePath string) string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Path == "" || modulePath == "" || !strings.HasPrefix(info.Path, modulePath) {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(info.Path, modulePath)))
}

// The Meta, index and constraint types are re-exported so that model files
// can declare Meta options without importing the migrations package.
type (
	// Meta declares model options gorm tags can't express.
	Meta = m.Meta
	// MetaProvider is implemented by models with Meta options.
	MetaProvider = m.MetaProvider
	// Index is a table index.
	Index = m.Index
	// IndexField is one element of an index.
	IndexField = m.IndexField
	// CheckConstraint is a CHECK constraint.
	CheckConstraint = m.CheckConstraint
	// UniqueConstraint is a multi-column unique constraint.
	UniqueConstraint = m.UniqueConstraint
	// ForeignKeyConstraint is a foreign key over one or more columns,
	// held as a table-level constraint.
	ForeignKeyConstraint = m.ForeignKeyConstraint
	// Constraint is what Meta.Constraints holds: a CheckConstraint, a
	// UniqueConstraint or a ForeignKeyConstraint. The interface is closed;
	// gormgate defines the constraint kinds.
	Constraint = m.Constraint
	// ClickHouseTable configures a ClickHouse table engine.
	ClickHouseTable = m.ClickHouseTable
	// SortOrder is the direction an index sorts a column in.
	SortOrder = m.SortOrder
	// Deferrable is when a deferrable constraint is checked.
	Deferrable = m.Deferrable
	// ReferentialAction is what a foreign key does to the referencing row.
	ReferentialAction = m.ReferentialAction
)

// The values those types take, re-exported for the same reason.
const (
	// SortAsc and SortDesc are the directions an index element can sort
	// in; the zero value leaves it to the database.
	SortAsc  = m.SortAsc
	SortDesc = m.SortDesc
	// Deferred and Immediate are when a deferrable unique constraint is
	// checked: at the end of the transaction, or at each statement.
	Deferred  = m.Deferred
	Immediate = m.Immediate
	// The referential actions of a ForeignKeyConstraint; the zero value
	// leaves the clause out, which is the database default.
	Cascade    = m.Cascade
	SetNull    = m.SetNull
	SetDefault = m.SetDefault
	Restrict   = m.Restrict
	NoAction   = m.NoAction
)

// Ptr returns a pointer to v, for the Meta fields whose zero value differs
// from "unset", such as Managed.
func Ptr[T any](v T) *T { return &v }
