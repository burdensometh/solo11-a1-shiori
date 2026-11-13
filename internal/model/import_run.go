package model

import "encoding/json"

// Reasons an import entry can be skipped. These are stored verbatim in the
// `runs` table and used to group the skipped entries in the import summary.
const (
	// SkipReasonInvalidDate means the entry's add/modification date could
	// not be parsed as a Unix timestamp.
	SkipReasonInvalidDate = "invalid_date"

	// SkipReasonInvalidURL means the entry's URL failed validation.
	SkipReasonInvalidURL = "invalid_url"

	// SkipReasonDuplicate means the URL already exists, either earlier in
	// the same import file or already stored in the database.
	SkipReasonDuplicate = "duplicate"

	// SkipReasonDBError means a database lookup failed while processing
	// the entry.
	SkipReasonDBError = "database_error"

	// SkipReasonSaveError means the entry was valid but could not be
	// persisted to the database.
	SkipReasonSaveError = "save_error"
)

// SkippedItem stores a single entry that could not be imported, together
// with the reason why and the original line of the import file it came
// from.
type SkippedItem struct {
	// Reason is one of the SkipReason* constants.
	Reason string `json:"reason" db:"-"`

	// Detail is an optional human readable explanation of the failure.
	Detail string `json:"detail,omitempty" db:"-"`

	// URL is the (possibly invalid) URL found in the entry, useful when
	// the original line cannot be reconstructed.
	URL string `json:"url,omitempty" db:"-"`

	// RawLine is the original line from the import file.
	RawLine string `json:"raw_line" db:"-"`
}

// ImportRun is the database representation of a single import execution.
type ImportRun struct {
	ID         DBID          `db:"id"          json:"id"`
	Source     string        `db:"source"      json:"source"`
	Kind       string        `db:"kind"        json:"kind"`
	Status     string        `db:"status"      json:"status"`
	Total      int           `db:"total"       json:"total"`
	Imported   int           `db:"imported"    json:"imported"`
	Skipped    []SkippedItem `db:"skipped"     json:"skipped"`
	StartedAt  string        `db:"started_at"  json:"started_at"`
	FinishedAt string        `db:"finished_at" json:"finished_at"`
	CreatedAt  string        `db:"created_at"  json:"created_at"`
}

// Import kind values stored in the `runs` table.
const (
	// ImportKindNetscape is the Netscape bookmark HTML format used by the
	// `import` command.
	ImportKindNetscape = "netscape"

	// ImportKindPocket is the Pocket HTML/CSV export format used by the
	// `pocket` command.
	ImportKindPocket = "pocket"
)

// Import status values stored in the `runs` table.
const (
	// ImportStatusCompleted means the import run reached the end, even if
	// some entries were skipped.
	ImportStatusCompleted = "completed"

	// ImportStatusFailed means the run aborted before completing, for
	// example because the bookmarks could not be saved.
	ImportStatusFailed = "failed"
)

// MarshalSkipped encodes the skipped items for storage in the JSON column
// of the `runs` table.
func MarshalSkipped(items []SkippedItem) (string, error) {
	if items == nil {
		items = []SkippedItem{}
	}

	data, err := json.Marshal(items)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// UnmarshalSkipped decodes the JSON content of the `skipped` column.
func UnmarshalSkipped(raw string) ([]SkippedItem, error) {
	items := []SkippedItem{}
	if raw == "" {
		return items, nil
	}

	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, err
	}

	return items, nil
}

// ImportResult is the structured result of an import run, returned to the
// caller and printed at the end of the import command.
type ImportResult struct {
	// Total is the number of entries found in the import file.
	Total int

	// Imported is the number of entries successfully stored in the
	// database.
	Imported int

	// Skipped is the list of entries that could not be imported, each one
	// carrying the reason and the original line.
	Skipped []SkippedItem
}

// SkippedByReason groups the skipped entries by their reason code.
func (r ImportResult) SkippedByReason() map[string][]SkippedItem {
	grouped := map[string][]SkippedItem{}
	for _, item := range r.Skipped {
		grouped[item.Reason] = append(grouped[item.Reason], item)
	}

	return grouped
}

// SkippedCount returns the number of skipped entries.
func (r ImportResult) SkippedCount() int {
	return len(r.Skipped)
}
