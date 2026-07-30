package mysqlstore

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"strings"
)

//go:embed migrations/*.sql
var migrations embed.FS

func Migrate(ctx context.Context, database *sql.DB) error {
	if database == nil {
		return fmt.Errorf("mysql database is nil")
	}
	content, err := migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		return fmt.Errorf("read mysql migration: %w", err)
	}
	for _, statement := range strings.Split(string(content), ";") {
		statement = strings.TrimSpace(statement)
		if statement == "" {
			continue
		}
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("execute mysql migration: %w", err)
		}
	}
	return nil
}
