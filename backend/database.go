package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const queryTimeout = 10 * time.Second

func openDatabase(driver, dsn string) (*sql.DB, error) {
	var db *sql.DB
	if driver == "postgres" {
		config, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, errors.New("invalid PostgreSQL database configuration")
		}
		db = stdlib.OpenDB(*config)
	} else {
		var err error
		db, err = sql.Open(driver, dsn)
		if err != nil {
			return nil, errors.New("invalid database configuration")
		}
	}
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(25)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)
	return db, nil
}

// NewAPI stores timestamps as whole Unix seconds. Round bounds up to
// maintain the same inclusive start and exclusive end as the requested range.
func unixBoundary(at time.Time) int64 {
	second := at.Unix()
	if at.Nanosecond() > 0 {
		second++
	}
	return second
}

func bindQuery(driver, query string) string {
	if driver != "postgres" {
		return query
	}
	var builder strings.Builder
	index := 0
	for _, char := range query {
		if char == '?' {
			index++
			fmt.Fprintf(&builder, "$%d", index)
			continue
		}
		builder.WriteRune(char)
	}
	return builder.String()
}

type queryDB struct {
	db     *sql.DB
	driver string
}

func (database queryDB) queryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return database.db.QueryRowContext(ctx, bindQuery(database.driver, query), args...)
}

func (database queryDB) query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return database.db.QueryContext(ctx, bindQuery(database.driver, query), args...)
}
