package management

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/autodetector"
	"github.com/ctolon/gormgate/internal/executor"
	"github.com/ctolon/gormgate/internal/graph"
	m "github.com/ctolon/gormgate/migrations"
)

// MigrateRun describes the migrate run a hook is called for: the app, the
// connection, the plan, the verbosity and whether the command may prompt.
type MigrateRun struct {
	App         string
	Conn        *base.Conn
	Plan        []m.PlanStep
	Verbosity   int
	Interactive bool
}

// MigrateHook runs before or after migrate for one app.
type MigrateHook func(MigrateRun) error

// migrateOptions are migrate's options.
type migrateOptions struct {
	database      string
	appLabel      string
	migrationName string
	fake          bool
	fakeInitial   bool
	plan          bool
	runSyncdb     bool
	// interactive is cleared by --no-input. migrate itself never prompts;
	// it passes the answer on to the pre- and post-migrate hooks.
	interactive bool
	check       bool
	prune       bool
}

// migrateCmd is one run of migrate.
type migrateCmd struct {
	ctx   *Context
	o     migrateOptions
	start time.Time
}

// newMigrateCmd builds the migrate command.
func newMigrateCmd(s *state) *cobra.Command {
	var (
		noInput bool
		o       migrateOptions
	)
	cmd := &cobra.Command{
		Use:   "migrate [app] [migration]",
		Short: "bring a database up to date with the migrations",
		Long: "Apply the migrations that have not been applied yet. Naming an app\n" +
			"limits the run to it; naming a migration moves that app to exactly\n" +
			"that point, forwards or backwards. The name zero unapplies them all.",
		Args: usageArgs(cobra.MaximumNArgs(2)),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.checkDatabase(o.database); err != nil {
				return err
			}
			if err := s.runChecks(); err != nil {
				return err
			}
			if len(args) > 0 {
				o.appLabel = args[0]
			}
			if len(args) > 1 {
				o.migrationName = args[1]
			}
			o.interactive = !noInput
			return (&migrateCmd{ctx: s.ctx, o: o}).run()
		},
	}
	databaseFlag(cmd, &o.database)
	f := cmd.Flags()
	f.BoolVarP(&noInput, "no-input", "y", false, "do not prompt; assume the answer that carries on")
	f.BoolVar(&o.fake, "fake", false, "record the migrations as applied without running them")
	f.BoolVar(&o.fakeInitial, "fake-initial", false, "record an initial migration as applied when its tables are already there")
	f.BoolVar(&o.plan, "plan", false, "list what would be applied, and stop")
	f.BoolVar(&o.runSyncdb, "run-syncdb", false, "create the tables of apps that have no migrations")
	f.BoolVar(&o.check, "check", false, "exit non-zero if anything is unapplied, and change nothing")
	f.BoolVar(&o.prune, "prune", false, "forget recorded migrations whose files are gone")
	return cmd
}

func (c *migrateCmd) out(msg string) { c.ctx.Stdout.Println(msg) }

// progress reports what the executor is doing, as it does it.
func (c *migrateCmd) progress(ev executor.Event, mig *m.Migration, fake bool) {
	if c.ctx.Verbosity < 1 {
		return
	}
	timed := c.ctx.Verbosity > 1
	elapsed := func() string {
		if !timed {
			return ""
		}
		return fmt.Sprintf(" (%.3fs)", time.Since(c.start).Seconds())
	}
	switch ev {
	case executor.ApplyStart, executor.UnapplyStart, executor.RenderStart:
		if timed {
			c.start = time.Now()
		}
		switch ev {
		case executor.ApplyStart:
			c.ctx.Stdout.Print(fmt.Sprintf("applying %s ... ", mig))
		case executor.UnapplyStart:
			c.ctx.Stdout.Print(fmt.Sprintf("unapplying %s ... ", mig))
		default:
			c.ctx.Stdout.Print("rendering model states ... ")
		}
	case executor.ApplySuccess, executor.UnapplySuccess:
		if fake {
			c.out(c.ctx.Style.Success("faked" + elapsed()))
		} else {
			c.out(c.ctx.Style.Success("ok" + elapsed()))
		}
	case executor.RenderSuccess:
		c.out(c.ctx.Style.Success("done" + elapsed()))
	}
}

// run applies the migrations.
func (c *migrateCmd) run() error {
	ctx := c.ctx
	conn, err := ctx.Project.Conn(c.o.database)
	if err != nil {
		return err
	}
	pre, err := ctx.Project.Loader(nil, false)
	if err != nil {
		return err
	}
	realModels, err := ctx.Project.RealModels(pre.UnmigratedApps)
	if err != nil {
		return err
	}
	ex, err := executor.New(conn, ctx.Project.LoaderConfig(nil, false), realModels, c.progress)
	if err != nil {
		return err
	}
	if err := ex.Loader.CheckConsistentHistory(ex.Recorder, conn.Alias()); err != nil {
		return err
	}
	if conflicts := ex.Loader.DetectConflicts(); len(conflicts) > 0 {
		var parts []string
		for _, app := range sortedMapKeys(conflicts) {
			parts = append(parts, fmt.Sprintf("%s in %s", strings.Join(conflicts[app], ", "), app))
		}
		return Errorf("conflicting migrations detected; multiple leaf nodes in the migration graph: (%s).\nTo fix them run '%s makemigrations --merge'", strings.Join(parts, "; "), ctx.Invocation())
	}
	runSyncdb := c.o.runSyncdb
	targetAppLabelsOnly := true
	appLabel := c.o.appLabel
	if appLabel != "" {
		if ctx.Project.App(appLabel) == nil {
			return Errorf("no app named %q; is it in the project's Apps?", appLabel)
		}
		if runSyncdb {
			if ex.Loader.MigratedApps[appLabel] {
				return Errorf("can't use run_syncdb with app '%s' as it has migrations", appLabel)
			}
		} else if !ex.Loader.MigratedApps[appLabel] {
			return Errorf("app '%s' does not have migrations", appLabel)
		}
	}
	var targets []m.Key
	migrationName := c.o.migrationName
	switch {
	case appLabel != "" && migrationName != "":
		if migrationName == "zero" {
			targets = []m.Key{{App: appLabel, Name: ""}}
		} else {
			mig, err := findMigration(ex.Loader, appLabel, migrationName, "")
			if err != nil {
				return err
			}
			target := mig.Key()
			if !ex.Loader.Graph.Has(target) {
				if inc, ok := ex.Loader.Replacements[target]; ok {
					target = inc.Replaces[len(inc.Replaces)-1]
				}
			}
			targets = []m.Key{target}
		}
		targetAppLabelsOnly = false
	case appLabel != "":
		for _, k := range ex.Loader.Graph.LeafNodes("") {
			if k.App == appLabel {
				targets = append(targets, k)
			}
		}
	default:
		targets = ex.Loader.Graph.LeafNodes("")
	}
	if c.o.prune {
		if appLabel == "" {
			return Errorf("migrations can be pruned only when an app is specified")
		}
		if err := c.prune(ex, appLabel); err != nil {
			return err
		}
	}
	plan, err := ex.MigrationPlan(targets, false)
	if err != nil {
		return err
	}
	if c.o.plan {
		ctx.Stdout.PrintlnStyled("planned operations:", ctx.Style.MigrateLabel)
		if len(plan) == 0 {
			c.out("no planned migration operations")
		} else {
			for _, s := range plan {
				ctx.Stdout.PrintlnStyled(s.Migration.String(), ctx.Style.MigrateHeading)
				for _, op := range s.Migration.Operations {
					msg, isErr := DescribeOperation(op, s.Backwards)
					var style Styler
					if isErr {
						style = ctx.Style.Warning
					}
					ctx.Stdout.PrintlnStyled("  "+msg, style)
				}
			}
			if c.o.check {
				return Silent(ExitError, "there are unapplied migrations")
			}
		}
		return nil
	}
	if c.o.check {
		if len(plan) > 0 {
			return Silent(ExitError, "there are unapplied migrations")
		}
		return nil
	}
	if c.o.prune {
		return nil
	}
	syncdb := runSyncdb && len(ex.Loader.UnmigratedApps) > 0
	// What is about to happen is detail: the applying lines below say what
	// actually happened, and they are the point of the command.
	if c.ctx.Verbosity >= 2 {
		if syncdb {
			apps := appLabel
			if apps == "" {
				apps = strings.Join(sortedMapKeys(ex.Loader.UnmigratedApps), ", ")
			}
			c.out(ctx.Style.MigrateLabel("syncing: ") + apps)
		}
		switch {
		case targetAppLabelsOnly:
			apps := map[string]bool{}
			for _, t := range targets {
				apps[t.App] = true
			}
			list := strings.Join(sortedMapKeys(apps), ", ")
			if list == "" {
				list = "nothing"
			}
			c.out(ctx.Style.MigrateLabel("target: ") + list + ", latest")
		case targets[0].Name == "":
			c.out(ctx.Style.MigrateLabel("target: ") + targets[0].App + ", zero")
		default:
			c.out(ctx.Style.MigrateLabel("target: ") + targets[0].App + ", " + targets[0].Name)
		}
	}
	preState, err := ex.CreateProjectState(true)
	if err != nil {
		return err
	}
	// Rendering the pre-migrate state here is what makes Django skip the
	// "Rendering model states..." progress line during migrate.
	if _, err := preState.Apps(); err != nil {
		return err
	}
	if err := c.emitMigrateHooks("pre", conn, plan); err != nil {
		return err
	}
	if syncdb {
		if c.ctx.Verbosity >= 1 {
			c.out(ctx.Style.MigrateHeading("synchronizing apps without migrations"))
		}
		labels := ex.Loader.UnmigratedApps
		if appLabel != "" {
			labels = map[string]bool{appLabel: true}
		}
		if err := c.syncApps(conn, labels); err != nil {
			return err
		}
	}
	fake, fakeInitial := c.o.fake, c.o.fakeInitial
	if len(plan) == 0 {
		if c.ctx.Verbosity >= 1 {
			c.out("no migrations to apply")
			to, err := ctx.Project.CurrentState()
			if err != nil {
				return err
			}
			from, err := ex.Loader.ProjectState(nil, true, realModels)
			if err != nil {
				return err
			}
			changes, err := autodetector.New(from, to, nil).ChangesFor(ex.Loader.Graph, autodetector.ChangesOptions{})
			if err != nil {
				return err
			}
			if len(changes) > 0 {
				var quoted []string
				for _, app := range sortedMapKeys(changes) {
					quoted = append(quoted, fmt.Sprintf("%q", app))
				}
				c.out(ctx.Style.Notice(fmt.Sprintf("models in %s have changes that no migration carries yet, so they were not applied", strings.Join(quoted, ", "))))
				c.out(ctx.Style.Notice(fmt.Sprintf("run '%s makemigrations', then '%s migrate' again", ctx.Invocation(), ctx.Invocation())))
			}
		}
		fake, fakeInitial = false, false
	}
	if plan == nil {
		// A nil plan would make the executor work one out; migrate has
		// already decided there is nothing to run.
		plan = []m.PlanStep{}
	}
	if _, err := ex.MigrateTo(executor.MigrateOptions{
		Targets: targets, Plan: plan, State: preState.Clone(), Fake: fake, FakeInitial: fakeInitial,
	}); err != nil {
		return err
	}
	if err := c.emitMigrateHooks("post", conn, plan); err != nil {
		return err
	}
	return nil
}

// emitMigrateHooks is emit_pre_migrate_signal / emit_post_migrate_signal.
func (c *migrateCmd) emitMigrateHooks(kind string, conn *base.Conn, plan []m.PlanStep) error {
	hooks := c.ctx.Project.PreMigrateHooks
	if kind == "post" {
		hooks = c.ctx.Project.PostMigrateHooks
	}
	for _, a := range c.ctx.Project.Apps {
		if c.ctx.Verbosity >= 2 {
			c.out(fmt.Sprintf("Running %s-migrate handlers for application %s", kind, a.Label))
		}
		run := MigrateRun{
			App: a.Label, Conn: conn, Plan: plan,
			Verbosity: c.ctx.Verbosity, Interactive: c.o.interactive,
		}
		for _, h := range hooks[a.Label] {
			if err := h(run); err != nil {
				return err
			}
		}
	}
	return nil
}

// prune deletes applied records of migrations that no longer exist.
func (c *migrateCmd) prune(ex *executor.Executor, app string) error {
	if c.ctx.Verbosity > 0 {
		c.ctx.Stdout.PrintlnStyled("Pruning migrations:", c.ctx.Style.MigrateHeading)
	}
	var toPrune []m.Key
	for _, k := range ex.Loader.AppliedOrder {
		if _, ok := ex.Loader.DiskMigrations[k]; !ok && k.App == app {
			toPrune = append(toPrune, k)
		}
	}
	slices.SortFunc(toPrune, func(a, b m.Key) int { return cmp.Compare(a.Name, b.Name) })
	pruned := map[m.Key]bool{}
	for _, k := range toPrune {
		pruned[k] = true
	}
	var squashed []m.Key
	for _, k := range replacementKeys(ex.Loader.Replacements) {
		for _, r := range ex.Loader.Replacements[k].Replaces {
			if pruned[r] {
				squashed = append(squashed, k)
				break
			}
		}
	}
	if len(squashed) > 0 {
		c.out(c.ctx.Style.Notice("cannot prune: these squashed migrations still carry Replaces, so they may not be recorded as applied:"))
		for _, k := range squashed {
			c.out(fmt.Sprintf("  %s.%s", k.App, k.Name))
		}
		c.out(c.ctx.Style.Notice(fmt.Sprintf("run '%s migrate' again if they are not marked applied, then drop Replaces from their Migration structs", c.ctx.Invocation())))
		return nil
	}
	if len(toPrune) == 0 {
		if c.ctx.Verbosity > 0 {
			c.out("no migrations to prune")
		}
		return nil
	}
	for _, k := range toPrune {
		if c.ctx.Verbosity > 0 {
			c.ctx.Stdout.Print(c.ctx.Style.MigrateLabel(fmt.Sprintf("pruning %s.%s ... ", k.App, k.Name)))
		}
		if err := ex.Recorder.RecordUnapplied(k.App, k.Name); err != nil {
			return err
		}
		if c.ctx.Verbosity > 0 {
			c.out(c.ctx.Style.Success("ok"))
		}
	}
	return nil
}

// replacementKeys returns the keys of the squashed migrations, in the
// (app, name) order the graph sorts by.
func replacementKeys(r map[m.Key]*m.Migration) []m.Key {
	keys := slices.Collect(maps.Keys(r))
	graph.SortKeys(keys)
	return keys
}

// syncApps creates the tables of apps without migrations.
func (c *migrateCmd) syncApps(conn *base.Conn, labels map[string]bool) error {
	st, err := c.ctx.Project.CurrentState()
	if err != nil {
		return err
	}
	tables, err := conn.Introspection().TableNames(false)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, t := range tables {
		have[t.Name] = true
	}
	apps, err := st.Apps()
	if err != nil {
		return err
	}
	if c.ctx.Verbosity >= 1 {
		c.out("creating tables")
	}
	ed := conn.Backend.NewEditor(conn, false, true)
	if err := ed.Begin(); err != nil {
		return err
	}
	var runErr error
	for _, a := range c.ctx.Project.Apps {
		if !labels[a.Label] {
			continue
		}
		// Django's sync_apps walks get_models(include_auto_created=False),
		// so the tables are created in the order the app declares its
		// models and join tables are left out.
		for _, model := range declaredModels(a, apps) {
			if model.Options().AutoCreated {
				continue
			}
			if have[conn.Introspection().IdentifierConverter(model.Table)] {
				continue
			}
			if !model.Managed() || !conn.AllowMigrate(model.App, m.Hints{Model: model, ModelName: strings.ToLower(model.Name)}) {
				continue
			}
			if c.ctx.Verbosity >= 3 {
				c.out(fmt.Sprintf("  processing %s.%s", a.Label, model.Name))
			}
			if c.ctx.Verbosity >= 1 {
				c.out("  creating table " + model.Table)
			}
			if runErr = ed.CreateModel(model); runErr != nil {
				break
			}
		}
		if runErr != nil {
			break
		}
	}
	if runErr == nil && c.ctx.Verbosity >= 1 {
		c.out("  running deferred SQL")
	}
	return ed.Finish(runErr)
}

// DescribeOperation describes an operation for --plan and reports whether
// running it in this direction is impossible.
func DescribeOperation(op m.Operation, backwards bool) (description string, irreversible bool) {
	// action is the code or SQL the operation would run in this
	// direction; it is absent when the operation cannot run backwards.
	var action string
	haveAction := true
	prefix := ""
	switch o := op.(type) {
	case *m.RunGo:
		code := o.Code
		if backwards {
			code = o.ReverseCode
		}
		// Go functions have no docstring, so there is nothing to show.
		haveAction = code != nil
	case *m.RunSQL:
		sql := o.SQL
		if backwards {
			sql = o.ReverseSQL
		}
		haveAction = sql != nil
		if haveAction {
			action = pythonSQLText(sql)
		}
	default:
		if backwards {
			prefix = "Undo "
		}
	}
	suffix := strings.ReplaceAll(action, "\n", "")
	if !haveAction {
		if backwards {
			suffix, irreversible = "IRREVERSIBLE", true
		} else {
			suffix = ""
		}
	}
	if suffix != "" {
		suffix = " -> " + suffix
	}
	return prefix + op.Describe() + truncateChars(suffix, 40), irreversible
}

// pythonSQLText renders a RunSQL SQL argument the way Python's str() would,
// for the one-line summary "migrate --plan" prints.
func pythonSQLText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []string:
		return pythonListRepr(x)
	case []m.SQLParams:
		parts := make([]string, len(x))
		for i, s := range x {
			ps := make([]string, len(s.Params))
			for j, p := range s.Params {
				ps[j] = fmt.Sprintf("%v", p)
			}
			parts[i] = fmt.Sprintf("%q with [%s]", s.SQL, strings.Join(ps, ", "))
		}
		return "[" + strings.Join(parts, ", ") + "]"
	}
	return fmt.Sprint(v)
}

// truncateChars is Truncator(text).chars(n): at most n characters,
// including the trailing ellipsis.
func truncateChars(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}
