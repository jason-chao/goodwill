// Package store owns the SQLite database: schema, writes and queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is a handle on the database. Writes go through a single connection;
// reads use a small separate pool so the dashboard never blocks ingestion.
type Store struct {
	w *sql.DB
	r *sql.DB

	writer *writer

	siteMu     sync.Mutex
	siteCache  map[string]*Site
	siteLoaded int64
}

// Open opens (and creates if needed) the database at path and applies any
// pending migrations.
func Open(path string) (*Store, error) {
	w, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := w.Ping(); err != nil {
		w.Close()
		return nil, fmt.Errorf("open database %s: %w", path, err)
	}
	if err := migrate(w); err != nil {
		w.Close()
		return nil, err
	}
	r, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(3)
	return &Store{w: w, r: r}, nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	// Page cache is per connection: 4 connections x 4 MB.
	q.Add("_pragma", "cache_size(-4000)")
	if readOnly {
		q.Add("_pragma", "query_only(1)")
	}
	return "file:" + path + "?" + q.Encode()
}

// Close flushes queued events and closes the database.
func (s *Store) Close() error {
	if s.writer != nil {
		s.writer.stop()
	}
	// Fold the write-ahead log back into the main file on a clean exit.
	s.w.Exec("pragma wal_checkpoint(TRUNCATE)")
	return errors.Join(s.r.Close(), s.w.Close())
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`create table if not exists schema_migrations (version integer primary key)`); err != nil {
		return err
	}
	var current int
	if err := db.QueryRow(`select coalesce(max(version), 0) from schema_migrations`).Scan(&current); err != nil {
		return err
	}
	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		base := name[strings.LastIndex(name, "/")+1:]
		version, err := strconv.Atoi(strings.SplitN(base, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("migration %s: bad version prefix", base)
		}
		if version <= current {
			continue
		}
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", base, err)
		}
		if _, err := tx.Exec(`insert into schema_migrations (version) values (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// SchemaVersion reports the latest applied migration.
func (s *Store) SchemaVersion() (int, error) {
	var v int
	err := s.r.QueryRow(`select coalesce(max(version), 0) from schema_migrations`).Scan(&v)
	return v, err
}

// Backup writes a consistent copy of the database to dest.
func (s *Store) Backup(ctx context.Context, dest string) error {
	_, err := s.w.ExecContext(ctx, `vacuum into ?`, dest)
	return err
}

// GetKV returns a stored value, or nil if the key is absent.
func (s *Store) GetKV(key string) ([]byte, error) {
	var v []byte
	err := s.r.QueryRow(`select value from kv where key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func (s *Store) SetKV(key string, value []byte) error {
	_, err := s.w.Exec(`insert into kv (key, value) values (?, ?)
		on conflict (key) do update set value = excluded.value`, key, value)
	return err
}

func (s *Store) DeleteKV(key string) error {
	_, err := s.w.Exec(`delete from kv where key = ?`, key)
	return err
}
