package management

import (
	"os"

	"github.com/ctolon/gormgate/backends/base"
	"github.com/ctolon/gormgate/internal/executor"
	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// writeSQL prints a command's generated SQL on stdout. When frame is set
// the script is wrapped in the connection's transaction statements, which
// a caller asks for only when the script is safe to run that way. An empty
// script prints nothing, so a command that found nothing to do stays
// silent.
func writeSQL(c *Context, alias, sql string, frame bool) error {
	if sql == "" {
		return nil
	}
	if !frame {
		c.Stdout.Println(sql)
		return nil
	}
	start, end, err := transactionSQL(c, alias)
	if err != nil {
		return err
	}
	if start != "" {
		c.Stdout.Println(c.Style.SQLKeyword(start))
	}
	c.Stdout.Println(sql)
	if end != "" {
		c.Stdout.Println(c.Style.SQLKeyword(end))
	}
	return nil
}

// newRecorder builds the migrations recorder of a connection, as the
// applied-migrations source the loader and the commands take.
func newRecorder(c *base.Conn) loader.Applied { return recorder.New(c) }

// transactionSQL returns the statements that open and close a transaction
// on the named connection.
func transactionSQL(c *Context, alias string) (string, string, error) {
	conn, err := c.Project.Conn(alias)
	if err != nil {
		return "", "", err
	}
	return conn.Backend.Ops.StartTransactionSQL(), conn.Backend.Ops.EndTransactionSQL(), nil
}

// writeFile writes a generated migration or command file, so that every
// file gormgate generates gets the same mode.
func writeFile(path string, content []byte) error {
	return os.WriteFile(path, content, 0o644)
}

// collectSQL returns the SQL a plan would run on conn, without running it.
// It is the commands' entry to executor.CollectSQL: they hold a loader but
// build no Executor, so they supply the real models themselves.
func collectSQL(ctx *Context, conn *base.Conn, l *loader.Loader, plan []m.PlanStep) ([]string, error) {
	realModels, err := ctx.Project.RealModels(l.UnmigratedApps)
	if err != nil {
		return nil, err
	}
	return executor.CollectSQL(conn, l, realModels, plan)
}
