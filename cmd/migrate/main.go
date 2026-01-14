package main

import (
	"database/sql"
	"log/slog"
	"os"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/mysql"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	dbDSN := os.Getenv("JOB_DB_DSN")
	if dbDSN == "" {
		fatal("JOB_DB_DSN is required")
	}

	migrationsPath := os.Getenv("MIGRATIONS_PATH")
	if migrationsPath == "" {
		migrationsPath = "migrations"
	}

	db, err := sql.Open("mysql", dbDSN)
	if err != nil {
		fatal("failed to open job db", "err", err)
	}
	defer db.Close()

	driver, err := mysql.WithInstance(db, &mysql.Config{})
	if err != nil {
		fatal("failed to create migration driver", "err", err)
	}

	m, err := migrate.NewWithDatabaseInstance("file://"+migrationsPath, "mysql", driver)
	if err != nil {
		fatal("failed to create migration", "err", err)
	}

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		fatal("migration failed", "err", err)
	}

	slog.Info("migration completed")
}

func fatal(msg string, attrs ...any) {
	slog.Error(msg, attrs...)
	os.Exit(1)
}
