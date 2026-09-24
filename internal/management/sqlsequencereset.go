package management

import (
	"reflect"
	"strings"

	"github.com/spf13/cobra"

	m "github.com/ctolon/gormgate/migrations"
)

// newSQLSequenceResetCmd builds the sqlsequencereset command.
func newSQLSequenceResetCmd(s *state) *cobra.Command {
	var database string
	cmd := &cobra.Command{
		Use:   "sqlsequencereset app [app ...]",
		Short: "print the SQL that resets the sequences of the given apps",
		Long: "Print the SQL statements that reset the primary key sequences of\n" +
			"every model in the given apps, for a database that has sequences.",
		Args: usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.checkDatabase(database); err != nil {
				return err
			}
			if err := s.runChecks(); err != nil {
				return err
			}
			return sqlSequenceReset(s.ctx, database, args)
		},
	}
	databaseFlag(cmd, &database)
	return cmd
}

// sqlSequenceReset prints the sequence-reset SQL of each app, wrapped in
// the connection's transaction statements.
func sqlSequenceReset(ctx *Context, alias string, labels []string) error {
	for _, l := range labels {
		if ctx.Project.App(l) == nil {
			return Errorf("no app named %q; is it in the project's Apps?", l)
		}
	}
	conn, err := ctx.Project.Conn(alias)
	if err != nil {
		return err
	}
	st, err := ctx.Project.CurrentState()
	if err != nil {
		return err
	}
	apps, err := st.Apps()
	if err != nil {
		return err
	}
	var output []string
	for _, label := range labels {
		if len(ctx.Project.App(label).Models) == 0 {
			continue
		}
		models := declaredModels(ctx.Project.App(label), apps)
		stmts, err := conn.Backend.SequenceResetSQL(conn, ctx.Style, models)
		if err != nil {
			return err
		}
		if len(stmts) == 0 && ctx.Verbosity >= 1 {
			ctx.Stderr.Println("no sequences found")
		}
		if out := strings.Join(stmts, "\n"); out != "" {
			output = append(output, out)
		}
	}
	return writeSQL(ctx, alias, strings.Join(output, "\n"), true)
}

// declaredModels lists an app's models in the order Django would.
//
// Django walks app_config.get_models(), which yields them in the order they
// were registered -- the order they appear in models.py. The equivalent
// here is the order they were passed to gormgate.App, since that is where a
// project declares them. Models that exist in the state but were never
// declared are the join tables gorm creates for a many2many; they follow,
// in the state's own order.
func declaredModels(app *App, apps *m.Apps) []*m.Model {
	out := make([]*m.Model, 0, len(app.Models))
	declared := make(map[string]bool, len(app.Models))
	for _, model := range app.Models {
		t := reflect.Indirect(reflect.ValueOf(model)).Type()
		name := strings.ToLower(t.Name())
		found, err := apps.GetModel(app.Label, name)
		if err != nil {
			// Declared but not in the state: its migration has not been
			// written yet, so it has no table to reset.
			continue
		}
		out = append(out, found)
		declared[name] = true
	}
	for _, model := range apps.Models() {
		if model.App == app.Label && !declared[strings.ToLower(model.Name)] {
			out = append(out, model)
		}
	}
	return out
}
