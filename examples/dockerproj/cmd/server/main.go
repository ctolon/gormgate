// Command server is the application. It reads and writes orders and it
// never migrates: the schema is somebody else's job, done before this
// process starts. All it does about the schema is refuse to start when the
// migration step has not run.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gorm.io/gorm"

	"example.com/dockerproj/internal/store"
	"example.com/dockerproj/shop"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func run() error {
	db, err := store.Open()
	if err != nil {
		return err
	}
	if err := checkSchema(db); err != nil {
		return err
	}

	addr := os.Getenv("DOCKERPROJ_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           routes(db),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			log.Println("shutdown:", err)
		}
	}()

	log.Println("listening on", addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// checkSchema refuses to serve against a database the migrations have not
// been run on. Without it the first request would fail with an obscure
// "relation does not exist", which is a much worse way to find out.
func checkSchema(db *gorm.DB) error {
	if !db.Migrator().HasTable(&shop.Order{}) {
		return errors.New("the database is not migrated: run `docker compose run --rm migrate` first")
	}
	return nil
}
