-- Pending deletions of bookmark files on disk. A row is written before the
-- bookmark record is removed, and deleted once every file of that bookmark
-- has been successfully removed from the data directory.
CREATE TABLE IF NOT EXISTS cleanup_tasks(
    id INTEGER NOT NULL,
    bookmark_id INTEGER NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT cleanup_tasks_PK PRIMARY KEY(id),
    CONSTRAINT cleanup_tasks_bookmark_id_UNIQUE UNIQUE(bookmark_id)
);
