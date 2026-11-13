package database

import (
	"context"
	"fmt"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/jmoiron/sqlx"
)

// cleanupTaskRow is the on-disk representation of a cleanup task.
type cleanupTaskRow struct {
	ID         model.DBID `db:"id"`
	BookmarkID int        `db:"bookmark_id"`
	CreatedAt  string     `db:"created_at"`
}

func (r cleanupTaskRow) toModel() model.CleanupTask {
	return model.CleanupTask{
		ID:         r.ID,
		BookmarkID: r.BookmarkID,
		CreatedAt:  r.CreatedAt,
	}
}

// SaveCleanupTask records a pending cleanup task for the bookmark ID.
// Recording the same bookmark again is a no-op, so a bookmark whose files
// could not be deleted keeps a single task across retries.
func (db *SQLiteDatabase) SaveCleanupTask(ctx context.Context, bookmarkID int) error {
	_, err := db.WriterDB().ExecContext(ctx,
		`INSERT INTO cleanup_tasks(bookmark_id) VALUES(?) ON CONFLICT DO NOTHING`, bookmarkID)
	if err != nil {
		return fmt.Errorf("failed to save cleanup task: %w", err)
	}
	return nil
}

// GetCleanupTasks returns every pending cleanup task, oldest first.
func (db *SQLiteDatabase) GetCleanupTasks(ctx context.Context) ([]model.CleanupTask, error) {
	return getCleanupTasks(ctx, db.ReaderDB())
}

// DeleteCleanupTask removes a task once all its files have been deleted.
func (db *SQLiteDatabase) DeleteCleanupTask(ctx context.Context, id model.DBID) error {
	return deleteCleanupTask(ctx, db.WriterDB(), "?", id)
}

// SaveCleanupTask records a pending cleanup task for the bookmark ID.
// Recording the same bookmark again is a no-op.
func (db *MySQLDatabase) SaveCleanupTask(ctx context.Context, bookmarkID int) error {
	_, err := db.WriterDB().ExecContext(ctx,
		`INSERT INTO cleanup_tasks(bookmark_id) VALUES(?)
		ON DUPLICATE KEY UPDATE bookmark_id = bookmark_id`, bookmarkID)
	if err != nil {
		return fmt.Errorf("failed to save cleanup task: %w", err)
	}
	return nil
}

// GetCleanupTasks returns every pending cleanup task, oldest first.
func (db *MySQLDatabase) GetCleanupTasks(ctx context.Context) ([]model.CleanupTask, error) {
	return getCleanupTasks(ctx, db.ReaderDB())
}

// DeleteCleanupTask removes a task once all its files have been deleted.
func (db *MySQLDatabase) DeleteCleanupTask(ctx context.Context, id model.DBID) error {
	return deleteCleanupTask(ctx, db.WriterDB(), "?", id)
}

// SaveCleanupTask records a pending cleanup task for the bookmark ID.
// Recording the same bookmark again is a no-op.
func (db *PGDatabase) SaveCleanupTask(ctx context.Context, bookmarkID int) error {
	_, err := db.WriterDB().ExecContext(ctx,
		`INSERT INTO cleanup_tasks(bookmark_id) VALUES($1) ON CONFLICT DO NOTHING`, bookmarkID)
	if err != nil {
		return fmt.Errorf("failed to save cleanup task: %w", err)
	}
	return nil
}

// GetCleanupTasks returns every pending cleanup task, oldest first.
func (db *PGDatabase) GetCleanupTasks(ctx context.Context) ([]model.CleanupTask, error) {
	return getCleanupTasks(ctx, db.ReaderDB())
}

// DeleteCleanupTask removes a task once all its files have been deleted.
func (db *PGDatabase) DeleteCleanupTask(ctx context.Context, id model.DBID) error {
	return deleteCleanupTask(ctx, db.WriterDB(), "$1", id)
}

func getCleanupTasks(ctx context.Context, queryer sqlx.QueryerContext) ([]model.CleanupTask, error) {
	rows := []cleanupTaskRow{}
	if err := sqlx.SelectContext(ctx, queryer, &rows,
		`SELECT id, bookmark_id, created_at FROM cleanup_tasks ORDER BY id`); err != nil {
		return nil, fmt.Errorf("failed to fetch cleanup tasks: %w", err)
	}

	tasks := make([]model.CleanupTask, 0, len(rows))
	for _, row := range rows {
		tasks = append(tasks, row.toModel())
	}

	return tasks, nil
}

func deleteCleanupTask(ctx context.Context, execer sqlx.ExecerContext, placeholder string, id model.DBID) error {
	if _, err := execer.ExecContext(ctx, `DELETE FROM cleanup_tasks WHERE id = `+placeholder, id); err != nil {
		return fmt.Errorf("failed to delete cleanup task: %w", err)
	}

	return nil
}
