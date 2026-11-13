package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-shiori/shiori/internal/database"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestDB(t *testing.T, ctx context.Context) model.DB {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "shiori-import-test-*")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(tmpDir) })

	db, err := database.OpenSQLiteDatabase(ctx, filepath.Join(tmpDir, "shiori.db"))
	require.NoError(t, err)
	require.NoError(t, db.Migrate(ctx))

	return db
}

// netscapeDocument contains two valid entries, one entry with an invalid
// date and one entry with an invalid URL.
const netscapeDocument = `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<META HTTP-EQUIV="Content-Type" CONTENT="text/html; charset=UTF-8">
<TITLE>Bookmarks</TITLE>
<H1>Bookmarks</H1>
<DL><p>
    <DT><A HREF="https://example.com/one" ADD_DATE="1700000000" LAST_MODIFIED="1700000000">One</A>
    <DT><A HREF="https://example.com/two" ADD_DATE="1700000100">Two</A>
    <DT><A HREF="https://example.com/bad-date" ADD_DATE="not-a-date">Bad date</A>
    <DT><A HREF="https:/broken-url">Broken URL</A>
</DL><p>
`

func TestParseNetscapeImport_collectsStructuredSkips(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	entries, result, err := parseNetscapeImport(ctx, db, strings.NewReader(netscapeDocument), false, newRerunFilter())
	require.NoError(t, err)

	assert.Len(t, entries, 2, "only the two valid entries survive parsing")
	assert.Equal(t, 4, result.Total, "every non-filtered entry is counted")
	assert.Equal(t, 0, result.Imported, "nothing is saved during parsing")

	byReason := result.SkippedByReason()
	require.Len(t, byReason[model.SkipReasonInvalidDate], 1)
	require.Len(t, byReason[model.SkipReasonInvalidURL], 1)
	assert.Equal(t, 2, result.SkippedCount())

	for _, item := range result.Skipped {
		assert.NotEmpty(t, item.RawLine, "the original line must be captured for every skipped entry")
		assert.True(t, strings.Contains(item.RawLine, "HREF") || strings.Contains(item.RawLine, "href"),
			"raw line should be the original anchor element")
	}
}

func TestExecuteImport_netscape_persistsRunWithSkippedLines(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	source := filepath.Join(t.TempDir(), "bookmarks.html")
	require.NoError(t, os.WriteFile(source, []byte(netscapeDocument), 0o600))

	run, saved, err := executeImport(ctx, db, source, model.ImportKindNetscape,
		func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
			return parseNetscapeImport(ctx, db, strings.NewReader(netscapeDocument), false, filter)
		})
	require.NoError(t, err)

	assert.Equal(t, model.ImportStatusCompleted, run.Status)
	assert.Equal(t, 4, run.Total)
	assert.Equal(t, 2, run.Imported)
	assert.Len(t, saved, 2)
	assert.Len(t, run.Skipped, 2)

	// The run must be persisted, with the raw lines in the skipped field.
	stored, exists, err := db.GetImportRun(ctx, run.ID)
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, 2, stored.Imported)
	assert.Len(t, stored.Skipped, 2)
	for _, item := range stored.Skipped {
		assert.NotEmpty(t, item.RawLine)
		assert.Contains(t, []string{model.SkipReasonInvalidDate, model.SkipReasonInvalidURL}, item.Reason)
	}

	// The valid bookmarks are in the database.
	bookmarks, err := db.GetBookmarks(ctx, model.DBGetBookmarksOptions{})
	require.NoError(t, err)
	assert.Len(t, bookmarks, 2)
}

func TestExecuteImport_rerunOnlyProcessesUnsuccessfulEntries(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	source := filepath.Join(t.TempDir(), "bookmarks.html")
	require.NoError(t, os.WriteFile(source, []byte(netscapeDocument), 0o600))

	parse := func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
		return parseNetscapeImport(ctx, db, strings.NewReader(netscapeDocument), false, filter)
	}

	firstRun, _, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse)
	require.NoError(t, err)
	require.Equal(t, 2, firstRun.Imported)
	require.Len(t, firstRun.Skipped, 2)

	// Second run: the two valid entries must be ignored, the two skipped
	// ones reprocessed and skipped again.
	secondRun, saved, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse)
	require.NoError(t, err)

	assert.Empty(t, saved, "already imported entries must not be processed again")
	assert.Equal(t, 2, secondRun.Total, "only previously unsuccessful entries are counted")
	assert.Equal(t, 0, secondRun.Imported)
	assert.Len(t, secondRun.Skipped, 2)

	// No extra bookmark was created.
	bookmarks, err := db.GetBookmarks(ctx, model.DBGetBookmarksOptions{})
	require.NoError(t, err)
	assert.Len(t, bookmarks, 2)

	// Both runs are kept for audit.
	runs, err := db.ListImportRuns(ctx, 10)
	require.NoError(t, err)
	assert.Len(t, runs, 2)
}

func TestExecuteImport_rerunPicksUpFixedEntries(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	source := filepath.Join(t.TempDir(), "bookmarks.html")

	brokenDoc := strings.Replace(netscapeDocument,
		`HREF="https://example.com/two" ADD_DATE="1700000100"`,
		`HREF="https://example.com/two" ADD_DATE="not-valid"`, 1)
	require.NoError(t, os.WriteFile(source, []byte(brokenDoc), 0o600))

	parse := func(doc string) func(*rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
		return func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
			return parseNetscapeImport(ctx, db, strings.NewReader(doc), false, filter)
		}
	}

	firstRun, _, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse(brokenDoc))
	require.NoError(t, err)
	assert.Equal(t, 1, firstRun.Imported)
	require.Len(t, firstRun.Skipped, 3)

	// Fix the document: the previously skipped entries are retried.
	require.NoError(t, os.WriteFile(source, []byte(netscapeDocument), 0o600))
	secondRun, saved, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse(netscapeDocument))
	require.NoError(t, err)

	// "two" is now valid and gets imported; the two genuinely broken
	// entries remain skipped.
	assert.Len(t, saved, 1)
	assert.Equal(t, 3, secondRun.Total)
	assert.Equal(t, 1, secondRun.Imported)
	assert.Len(t, secondRun.Skipped, 2)

	bookmarks, err := db.GetBookmarks(ctx, model.DBGetBookmarksOptions{})
	require.NoError(t, err)
	assert.Len(t, bookmarks, 2)
}

func TestParseNetscapeImport_duplicatesAreClassified(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	doc := `<!DOCTYPE NETSCAPE-Bookmark-file-1>
<DL><p>
    <DT><A HREF="https://example.com/dup" ADD_DATE="1700000000">First</A>
    <DT><A HREF="https://example.com/dup" ADD_DATE="1700000001">Second</A>
</DL><p>`

	_, result, err := parseNetscapeImport(ctx, db, strings.NewReader(doc), false, newRerunFilter())
	require.NoError(t, err)
	assert.Equal(t, 2, result.Total)
	assert.Equal(t, 1, result.SkippedCount())
	require.Len(t, result.SkippedByReason()[model.SkipReasonDuplicate], 1)
	assert.NotEmpty(t, result.Skipped[0].RawLine)

	// A second run against a database that already has the URL skips it
	// as a duplicate too.
	_, _, err = executeImport(ctx, db, "dup.html", model.ImportKindNetscape,
		func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
			return parseNetscapeImport(ctx, db, strings.NewReader(doc), false, filter)
		})
	require.NoError(t, err)
}

const pocketCSV = "title,url,time_added,tags\n" +
	"Shiori,https://github.com/go-shiori/shiori,1700000000,shiori\n" +
	"Bad date,https://example.com/bad-date,not-a-date,\n" +
	"Broken,https:/broken-url,1700000000,\n"

func TestParsePocketCSVExport_structuredSkips(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	srcFile, err := os.CreateTemp("", "pocket-*.csv")
	require.NoError(t, err)
	defer os.Remove(srcFile.Name())
	_, err = srcFile.WriteString(pocketCSV)
	require.NoError(t, err)
	_, err = srcFile.Seek(0, 0)
	require.NoError(t, err)

	entries, result, err := parsePocketCSVExport(ctx, db, srcFile, newRerunFilter())
	require.NoError(t, err)
	assert.Len(t, entries, 1)
	assert.Equal(t, 3, result.Total)

	byReason := result.SkippedByReason()
	assert.Len(t, byReason[model.SkipReasonInvalidDate], 1)
	assert.Len(t, byReason[model.SkipReasonInvalidURL], 1)
	for _, item := range result.Skipped {
		assert.NotEmpty(t, item.RawLine, "the original CSV row must be captured")
	}
}

func TestExecuteImport_pocketCSVRerun(t *testing.T) {
	ctx := context.TODO()
	db := newTestDB(t, ctx)

	source := filepath.Join(t.TempDir(), "pocket.csv")
	require.NoError(t, os.WriteFile(source, []byte(pocketCSV), 0o600))

	parse := func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
		file, err := os.Open(source)
		require.NoError(t, err)
		defer file.Close()
		return parsePocketCSVExport(ctx, db, file, filter)
	}

	firstRun, saved, err := executeImport(ctx, db, source, model.ImportKindPocket+"_csv", parse)
	require.NoError(t, err)
	assert.Len(t, saved, 1)
	assert.Equal(t, 3, firstRun.Total)
	assert.Len(t, firstRun.Skipped, 2)

	secondRun, saved, err := executeImport(ctx, db, source, model.ImportKindPocket+"_csv", parse)
	require.NoError(t, err)
	assert.Empty(t, saved)
	assert.Equal(t, 2, secondRun.Total)
	assert.Equal(t, 0, secondRun.Imported)
	assert.Len(t, secondRun.Skipped, 2)
}

// failingSaveDB wraps a real database and makes SaveBookmarks fail once,
// simulating a write failure during the import.
type failingSaveDB struct {
	model.DB
	failNextSave bool
}

func (f *failingSaveDB) SaveBookmarks(ctx context.Context, create bool, bookmarks ...model.BookmarkDTO) ([]model.BookmarkDTO, error) {
	if f.failNextSave {
		f.failNextSave = false
		return nil, errors.New("simulated write failure")
	}
	return f.DB.SaveBookmarks(ctx, create, bookmarks...)
}

func TestExecuteImport_failedSaveIsRecordedAndRetried(t *testing.T) {
	ctx := context.TODO()
	db := &failingSaveDB{DB: newTestDB(t, ctx), failNextSave: true}

	source := filepath.Join(t.TempDir(), "bookmarks.html")
	require.NoError(t, os.WriteFile(source, []byte(netscapeDocument), 0o600))

	parse := func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
		return parseNetscapeImport(ctx, db, strings.NewReader(netscapeDocument), false, filter)
	}

	// First run: the two valid entries fail to save and are recorded as
	// unsuccessful; the two invalid entries keep their original reasons.
	firstRun, saved, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse)
	require.NoError(t, err, "a failed save must not abort the run recording")
	assert.Nil(t, saved)
	assert.Equal(t, model.ImportStatusFailed, firstRun.Status)
	assert.Equal(t, 0, firstRun.Imported)
	assert.Len(t, firstRun.Skipped, 4)

	byReason := map[string]int{}
	for _, item := range firstRun.Skipped {
		byReason[item.Reason]++
	}
	assert.Equal(t, 2, byReason[model.SkipReasonSaveError])
	assert.Equal(t, 1, byReason[model.SkipReasonInvalidDate])
	assert.Equal(t, 1, byReason[model.SkipReasonInvalidURL])

	// Rerun: only the four previously unsuccessful entries are processed,
	// and the two valid ones now save successfully.
	secondRun, saved, err := executeImport(ctx, db, source, model.ImportKindNetscape, parse)
	require.NoError(t, err)
	assert.Equal(t, model.ImportStatusCompleted, secondRun.Status)
	assert.Equal(t, 4, secondRun.Total)
	assert.Len(t, saved, 2)
	assert.Equal(t, 2, secondRun.Imported)
	assert.Len(t, secondRun.Skipped, 2)
}

func TestMarshalUnmarshalSkipped(t *testing.T) {
	items := []model.SkippedItem{
		{Reason: model.SkipReasonInvalidURL, URL: "x", RawLine: `<a href="x">`},
	}

	raw, err := model.MarshalSkipped(items)
	require.NoError(t, err)

	decoded, err := model.UnmarshalSkipped(raw)
	require.NoError(t, err)
	assert.Equal(t, items, decoded)

	empty, err := model.UnmarshalSkipped("")
	require.NoError(t, err)
	assert.NotNil(t, empty)
	assert.Empty(t, empty)

	nilEncoded, err := model.MarshalSkipped(nil)
	require.NoError(t, err)
	assert.Equal(t, "[]", nilEncoded)
}
