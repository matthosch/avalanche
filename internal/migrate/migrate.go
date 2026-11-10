// Package migrate applies a service's embedded goose migrations to its Postgres database.
package migrate

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

// Up brings the database up to date with the migrations in dir, naming them name in errors.
// It is safe to call from several processes at once: a Postgres advisory lock lets one apply
// the migrations while the others wait, then find nothing left to do.
func Up(ctx context.Context, pool *pgxpool.Pool, name string, dir fs.FS) error {
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	// Poll for the lock every second, for up to a minute, rather than goose's 5 s default, so a
	// process that loses the race starts about as soon as the winner has finished.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 60))
	if err != nil {
		return fmt.Errorf("%s migrations lock: %w", name, err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, dir, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("%s migrations: %w", name, err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("%s migrate up: %w", name, err)
	}
	return nil
}

// Sub returns the subdirectory dir of an embedded migrations filesystem.
func Sub(fsys fs.FS, dir string) fs.FS {
	sub, err := fs.Sub(fsys, dir)
	if err != nil {
		panic(err) // the embed path is fixed at compile time
	}
	return sub
}
