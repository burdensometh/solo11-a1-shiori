package database

import (
	"context"
	"testing"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestImportRuns(t *testing.T) {
	ctx := context.TODO()

	db, err := sqliteTestDatabaseFactory(t, ctx)
	require.NoError(t, err)

	run := model.ImportRun{
		Source:   "bookmarks.html",
		Kind:     model.ImportKindNetscape,
		Status:   model.ImportStatusCompleted,
		Total:    4,
		Imported: 2,
		Skipped: []model.SkippedItem{
			{
				Reason:  model.SkipReasonInvalidDate,
				Detail:  `strconv.Atoi: parsing "not-a-date": invalid syntax`,
				URL:     "https://example.com/bad-date",
				RawLine: `<a href="https://example.com/bad-date" add_date="not-a-date">Bad date</a>`,
			},
			{
				Reason:  model.SkipReasonInvalidURL,
				URL:     "https:/broken",
				RawLine: `<a href="https:/broken">Broken</a>`,
			},
		},
		StartedAt:  "2026-11-13 10:00:00",
		FinishedAt: "2026-11-13 10:00:01",
	}

	saved, err := db.SaveImportRun(ctx, run)
	require.NoError(t, err, "SaveImportRun must not fail")
	assert.NotZero(t, saved.ID, "saved run must have an ID")
	assert.Equal(t, run.Source, saved.Source)
	assert.Equal(t, run.Kind, saved.Kind)
	assert.Equal(t, run.Status, saved.Status)
	assert.Equal(t, run.Total, saved.Total)
	assert.Equal(t, run.Imported, saved.Imported)
	assert.NotEmpty(t, saved.CreatedAt, "created_at must be populated")
	require.Len(t, saved.Skipped, 2, "all skipped items must be stored")
	assert.Equal(t, model.SkipReasonInvalidDate, saved.Skipped[0].Reason)
	assert.Equal(t, run.Skipped[0].RawLine, saved.Skipped[0].RawLine, "the original raw line must be preserved")
	assert.Equal(t, model.SkipReasonInvalidURL, saved.Skipped[1].Reason)

	fetched, exists, err := db.GetImportRun(ctx, saved.ID)
	require.NoError(t, err)
	assert.True(t, exists)
	assert.Equal(t, saved.ID, fetched.ID)
	require.Len(t, fetched.Skipped, 2)
	assert.Equal(t, run.Skipped[0], fetched.Skipped[0])
	assert.Equal(t, run.Skipped[1], fetched.Skipped[1])

	missing, exists, err := db.GetImportRun(ctx, model.DBID(9999))
	require.NoError(t, err)
	assert.False(t, exists)
	assert.Zero(t, missing.ID)

	// Save a second run and verify newest-first ordering.
	second := model.ImportRun{
		Source:     "bookmarks.html",
		Kind:       model.ImportKindNetscape,
		Status:     model.ImportStatusFailed,
		Total:      1,
		StartedAt:  "2026-11-13 11:00:00",
		FinishedAt: "2026-11-13 11:00:01",
		Skipped:    []model.SkippedItem{},
	}
	secondSaved, err := db.SaveImportRun(ctx, second)
	require.NoError(t, err)

	runs, err := db.ListImportRuns(ctx, 10)
	require.NoError(t, err)
	require.Len(t, runs, 2)
	assert.Equal(t, secondSaved.ID, runs[0].ID, "newest run must come first")
	assert.Equal(t, saved.ID, runs[1].ID)
	assert.Empty(t, runs[0].Skipped, "empty skipped list must round-trip as an empty slice")
}
