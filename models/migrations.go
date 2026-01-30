package models

import (
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations
var migrationsFS embed.FS

// Migrate applies database migrations.
func Migrate(postgresConnStr string) error {
	sourceInstance, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", sourceInstance, postgresConnStr)
	if err != nil {
		return err
	}

	err = m.Up()
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}

	sourceErr, databaseErr := m.Close()
	if sourceErr != nil || databaseErr != nil {
		return errors.Join(sourceErr, databaseErr)
	}

	return nil
}
