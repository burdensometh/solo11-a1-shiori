package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/jmoiron/sqlx"
)

// importRunRow is the on-disk representation of an import run, with the
// skipped entries stored as a JSON string.
type importRunRow struct {
	ID         model.DBID `db:"id"`
	Source     string     `db:"source"`
	Kind       string     `db:"kind"`
	Status     string     `db:"status"`
	Total      int        `db:"total"`
	Imported   int        `db:"imported"`
	Skipped    string     `db:"skipped"`
	StartedAt  string     `db:"started_at"`
	FinishedAt string     `db:"finished_at"`
	CreatedAt  string     `db:"created_at"`
}

func importRunRowFromModel(run model.ImportRun) (importRunRow, error) {
	skipped, err := model.MarshalSkipped(run.Skipped)
	if err != nil {
		return importRunRow{}, fmt.Errorf("failed to encode skipped entries: %w", err)
	}

	return importRunRow{
		ID:         run.ID,
		Source:     run.Source,
		Kind:       run.Kind,
		Status:     run.Status,
		Total:      run.Total,
		Imported:   run.Imported,
		Skipped:    skipped,
		StartedAt:  run.StartedAt,
		FinishedAt: run.FinishedAt,
		CreatedAt:  run.CreatedAt,
	}, nil
}

func (r importRunRow) toModel() (model.ImportRun, error) {
	skipped, err := model.UnmarshalSkipped(r.Skipped)
	if err != nil {
		return model.ImportRun{}, fmt.Errorf("failed to decode skipped entries: %w", err)
	}

	return model.ImportRun{
		ID:         r.ID,
		Source:     r.Source,
		Kind:       r.Kind,
		Status:     r.Status,
		Total:      r.Total,
		Imported:   r.Imported,
		Skipped:    skipped,
		StartedAt:  r.StartedAt,
		FinishedAt: r.FinishedAt,
		CreatedAt:  r.CreatedAt,
	}, nil
}

// SaveImportRun implements model.DB for SQLite.
func (db *SQLiteDatabase) SaveImportRun(ctx context.Context, run model.ImportRun) (model.ImportRun, error) {
	row, err := importRunRowFromModel(run)
	if err != nil {
		return model.ImportRun{}, err
	}

	if err := db.withTxRetry(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowxContext(ctx, `INSERT INTO runs
			(source, kind, status, total, imported, skipped, started_at, finished_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?) RETURNING id, created_at`,
			row.Source, row.Kind, row.Status, row.Total, row.Imported,
			row.Skipped, row.StartedAt, row.FinishedAt).
			Scan(&row.ID, &row.CreatedAt)
	}); err != nil {
		return model.ImportRun{}, fmt.Errorf("failed to save import run: %w", err)
	}

	saved, err := row.toModel()
	if err != nil {
		return model.ImportRun{}, err
	}

	return saved, nil
}

// GetImportRun implements model.DB for SQLite.
func (db *SQLiteDatabase) GetImportRun(ctx context.Context, id model.DBID) (model.ImportRun, bool, error) {
	return getImportRun(ctx, db.ReaderDB(), "?", id)
}

// ListImportRuns implements model.DB for SQLite.
func (db *SQLiteDatabase) ListImportRuns(ctx context.Context, limit int) ([]model.ImportRun, error) {
	return listImportRuns(ctx, db.ReaderDB(), "?", limit)
}

// SaveImportRun implements model.DB for MySQL/MariaDB.
func (db *MySQLDatabase) SaveImportRun(ctx context.Context, run model.ImportRun) (model.ImportRun, error) {
	row, err := importRunRowFromModel(run)
	if err != nil {
		return model.ImportRun{}, err
	}

	if err := db.withTx(ctx, func(tx *sqlx.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO runs
			(source, kind, status, total, imported, skipped, started_at, finished_at)
			VALUES(?, ?, ?, ?, ?, ?, ?, ?)`,
			row.Source, row.Kind, row.Status, row.Total, row.Imported,
			row.Skipped, row.StartedAt, row.FinishedAt)
		if err != nil {
			return fmt.Errorf("failed to insert import run: %w", err)
		}

		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("failed to get import run id: %w", err)
		}

		row.ID = model.DBID(id)
		return tx.GetContext(ctx, &row.CreatedAt,
			"SELECT created_at FROM runs WHERE id = ?", row.ID)
	}); err != nil {
		return model.ImportRun{}, err
	}

	return row.toModel()
}

// GetImportRun implements model.DB for MySQL/MariaDB.
func (db *MySQLDatabase) GetImportRun(ctx context.Context, id model.DBID) (model.ImportRun, bool, error) {
	return getImportRun(ctx, db.ReaderDB(), "?", id)
}

// ListImportRuns implements model.DB for MySQL/MariaDB.
func (db *MySQLDatabase) ListImportRuns(ctx context.Context, limit int) ([]model.ImportRun, error) {
	return listImportRuns(ctx, db.ReaderDB(), "?", limit)
}

// SaveImportRun implements model.DB for PostgreSQL.
func (db *PGDatabase) SaveImportRun(ctx context.Context, run model.ImportRun) (model.ImportRun, error) {
	row, err := importRunRowFromModel(run)
	if err != nil {
		return model.ImportRun{}, err
	}

	if err := db.withTx(ctx, func(tx *sqlx.Tx) error {
		return tx.QueryRowxContext(ctx, `INSERT INTO runs
			(source, kind, status, total, imported, skipped, started_at, finished_at)
			VALUES($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
			row.Source, row.Kind, row.Status, row.Total, row.Imported,
			row.Skipped, row.StartedAt, row.FinishedAt).
			Scan(&row.ID, &row.CreatedAt)
	}); err != nil {
		return model.ImportRun{}, fmt.Errorf("failed to save import run: %w", err)
	}

	return row.toModel()
}

// GetImportRun implements model.DB for PostgreSQL.
func (db *PGDatabase) GetImportRun(ctx context.Context, id model.DBID) (model.ImportRun, bool, error) {
	return getImportRun(ctx, db.ReaderDB(), "$1", id)
}

// ListImportRuns implements model.DB for PostgreSQL.
func (db *PGDatabase) ListImportRuns(ctx context.Context, limit int) ([]model.ImportRun, error) {
	return listImportRuns(ctx, db.ReaderDB(), "$1", limit)
}

func getImportRun(ctx context.Context, queryer sqlx.QueryerContext, placeholder string, id model.DBID) (model.ImportRun, bool, error) {
	query := `SELECT id, source, kind, status, total, imported, skipped,
		started_at, finished_at, created_at
		FROM runs WHERE id = ` + placeholder

	var row importRunRow
	err := sqlx.GetContext(ctx, queryer, &row, query, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ImportRun{}, false, nil
		}
		return model.ImportRun{}, false, fmt.Errorf("failed to fetch import run: %w", err)
	}

	run, err := row.toModel()
	if err != nil {
		return model.ImportRun{}, false, err
	}

	return run, true, nil
}

func listImportRuns(ctx context.Context, queryer sqlx.QueryerContext, placeholder string, limit int) ([]model.ImportRun, error) {
	if limit <= 0 {
		limit = 50
	}

	query := `SELECT id, source, kind, status, total, imported, skipped,
		started_at, finished_at, created_at
		FROM runs ORDER BY id DESC LIMIT ` + placeholder

	var runRows []importRunRow
	if err := sqlx.SelectContext(ctx, queryer, &runRows, query, limit); err != nil {
		return nil, fmt.Errorf("failed to fetch import runs: %w", err)
	}

	runs := make([]model.ImportRun, 0, len(runRows))
	for _, row := range runRows {
		run, err := row.toModel()
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}

	return runs, nil
}
