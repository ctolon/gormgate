package management

import (
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// newSQLMigrateCmd builds the sqlmigrate command.
func newSQLMigrateCmd(s *state) *cobra.Command {
	var (
		database  string
		backwards bool
	)
	cmd := &cobra.Command{
		Use:   "sqlmigrate app migration",
		Short: "print the SQL of one migration without running it",
		Long: "Print the statements a migration would run against a database. The\n" +
			"migration is not applied and nothing is recorded.",
		Args: usageArgs(cobra.ExactArgs(2)),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.checkDatabase(database); err != nil {
				return err
			}
			if err := s.runChecks(); err != nil {
				return err
			}
			return sqlMigrate(s.ctx, database, args[0], args[1], backwards)
		},
	}
	databaseFlag(cmd, &database)
	cmd.Flags().BoolVar(&backwards, "backwards", false,
		"print the SQL that unapplies the migration instead")
	return cmd
}

// sqlMigrate prints the SQL of one migration.
func sqlMigrate(ctx *Context, alias, app, name string, backwards bool) error {
	conn, err := ctx.Project.Conn(alias)
	if err != nil {
		return err
	}
	cfg := ctx.Project.LoaderConfig(recorder.New(conn), false)
	cfg.ReplaceMigrations = false
	l, err := loader.New(cfg)
	if err != nil {
		return err
	}
	// sqlmigrate needs its own loader, with squashed migrations left
	// unreplaced, so the app check runs against that one rather than
	// building a second.
	if _, err := ctx.Project.checkMigratedApp(func() (*loader.Loader, error) { return l, nil }, app, ""); err != nil {
		return err
	}
	mig, err := findMigration(l, app, name, "; is it in the project's Apps?")
	if err != nil {
		return err
	}
	plan := []m.PlanStep{{Migration: l.Graph.Nodes[mig.Key()], Backwards: backwards}}
	sqls, err := collectSQL(ctx, conn, l, plan)
	if err != nil {
		return err
	}
	if len(sqls) == 0 && ctx.Verbosity >= 1 {
		ctx.Stderr.Println("no operations found")
	}
	// The statements are framed as a transaction only when running them
	// that way would work: an atomic migration on a backend that can roll
	// DDL back.
	frame := mig.IsAtomic() && conn.Backend.Features.CanRollbackDDL
	return writeSQL(ctx, alias, strings.Join(sqls, "\n"), frame)
}
