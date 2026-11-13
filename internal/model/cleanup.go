package model

// CleanupTask represents a pending deletion of the files stored on disk for a
// bookmark that has already been removed from the database.
//
// Deleting a bookmark row and deleting its files cannot happen atomically, so
// a deletion request is persisted as a task in the database before the record
// is removed. The task is only deleted once every file of the bookmark has
// been successfully removed from the data directory. Tasks left behind (because
// the files could not be removed or the process stopped halfway through) are
// retried the next time Shiori starts.
type CleanupTask struct {
	ID         DBID   `db:"id"          json:"id"`
	BookmarkID int    `db:"bookmark_id" json:"bookmark_id"`
	CreatedAt  string `db:"created_at"  json:"created_at"`
}
