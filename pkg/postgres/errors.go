package postgres

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// RowScanner abstracts a single SQL row scan (pgx.Row or pgx.Rows).
type RowScanner interface {
	Scan(dest ...any) error
}

// IsNoRows reports whether err is pgx.ErrNoRows.
func IsNoRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}

// IsUniqueViolation reports whether err is a PostgreSQL 23505 unique_violation.
func IsUniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// IsForeignKeyViolation reports whether err is a PostgreSQL 23503 foreign_key_violation.
func IsForeignKeyViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23503"
}

// IsRetryable reports whether err is a transient PostgreSQL connection, timeout, or serialization error.
func IsRetryable(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		code := pe.Code
		return strings.HasPrefix(code, "08") || strings.HasPrefix(code, "53") || code == "40001" || code == "40P01" || code == "57P01" || code == "57P02" || code == "57P03"
	}
	return pgconn.SafeToRetry(err) || pgconn.Timeout(err)
}
