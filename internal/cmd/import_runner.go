package cmd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/model"
)

// parsedImportEntry is a bookmark that survived validation, still paired
// with the original line it came from so that a later save failure can be
// recorded with that line.
type parsedImportEntry struct {
	Bookmark model.BookmarkDTO
	RawLine  string
}

// rerunFilter keeps the entries that were not successfully imported in a
// previous run of the same source. When active, every other entry is
// ignored instead of being processed again.
type rerunFilter struct {
	active bool
	// lines contains the exact raw lines recorded in the skipped entries
	// of the latest run.
	lines map[string]struct{}
	// urls contains the URLs of entries skipped because the entry itself
	// was invalid (bad date or bad URL). Those entries are matched by URL
	// as well, because fixing the file also changes their raw line.
	urls map[string]struct{}
}

func newRerunFilter() *rerunFilter {
	return &rerunFilter{
		lines: map[string]struct{}{},
		urls:  map[string]struct{}{},
	}
}

// allows reports whether the entry identified by its raw line and URL
// still needs to be processed.
func (f *rerunFilter) allows(rawLine, url string) bool {
	if !f.active {
		return true
	}

	if _, ok := f.lines[rawLine]; ok {
		return true
	}

	// Only entries that were invalid on their own are retried by URL:
	// their content may have been fixed, which also changes the raw line.
	// Duplicates are never retried by URL because their URL belongs to an
	// entry that was imported successfully.
	if url != "" {
		if _, ok := f.urls[url]; ok {
			return true
		}
	}

	return false
}

// loadLatestSkipped populates the filter from the most recent run recorded
// for the given source. If there is no such run, or it has no skipped
// entries, the filter stays inactive and a full import is performed.
func (f *rerunFilter) loadLatestSkipped(ctx context.Context, db model.DB, source string) error {
	runs, err := db.ListImportRuns(ctx, 1000)
	if err != nil {
		return fmt.Errorf("failed to look up previous import runs: %w", err)
	}

	for _, run := range runs {
		if run.Source != source || len(run.Skipped) == 0 {
			continue
		}

		f.active = true
		for _, item := range run.Skipped {
			if item.RawLine != "" {
				f.lines[item.RawLine] = struct{}{}
			}
			if item.URL != "" && isRetriableByURL(item.Reason) {
				f.urls[item.URL] = struct{}{}
			}
		}
		break
	}

	return nil
}

// isRetriableByURL reports whether entries skipped for the given reason
// may be matched by URL on a rerun, in addition to exact raw-line
// matching.
func isRetriableByURL(reason string) bool {
	switch reason {
	case model.SkipReasonInvalidDate,
		model.SkipReasonInvalidURL,
		model.SkipReasonDBError,
		model.SkipReasonSaveError:
		return true
	default:
		return false
	}
}

// skip records an entry that could not be imported.
func skip(result *model.ImportResult, reason, detail, url, rawLine string) {
	result.Skipped = append(result.Skipped, model.SkippedItem{
		Reason:  reason,
		Detail:  detail,
		URL:     url,
		RawLine: rawLine,
	})
	cError.Printf("Skip %s: %s\n", fallbackText(url, rawLine), skipReasonLabel(reason, detail))
}

func fallbackText(url, rawLine string) string {
	if url != "" {
		return url
	}
	return rawLine
}

// skipReasonLabel renders a human readable explanation for a skip reason.
func skipReasonLabel(reason, detail string) string {
	var label string
	switch reason {
	case model.SkipReasonInvalidDate:
		label = "date field is not valid"
	case model.SkipReasonInvalidURL:
		label = "URL is not valid"
	case model.SkipReasonDuplicate:
		label = "URL already exists"
	case model.SkipReasonDBError:
		label = "database lookup failed"
	case model.SkipReasonSaveError:
		label = "failed to save bookmark"
	default:
		label = reason
	}

	if detail != "" {
		return label + ": " + detail
	}

	return label
}

// parseNetscapeImport parses a Netscape bookmark HTML document, validating
// every entry and collecting structured information about the skipped
// ones. Entries already imported in a previous run of the same source are
// ignored when the rerun filter is active.
func parseNetscapeImport(ctx context.Context, db model.DB, reader io.Reader, generateTag bool, filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
	result := model.ImportResult{}
	entries := []parsedImportEntry{}
	seenURLs := map[string]struct{}{}

	doc, err := goquery.NewDocumentFromReader(reader)
	if err != nil {
		return nil, result, fmt.Errorf("failed to parse bookmark: %w", err)
	}

	doc.Find("dt>a").Each(func(_ int, a *goquery.Selection) {
		// The serialized anchor is the best available reconstruction of
		// the original line, since the HTML parser drops line numbers.
		rawLine, _ := goquery.OuterHtml(a)
		rawLine = strings.TrimSpace(rawLine)

		dt := a.Parent()
		dl := dt.Parent()
		h3 := dl.Parent().Find("h3").First()

		title := a.Text()
		url, _ := a.Attr("href")
		strTags, _ := a.Attr("tags")

		dateStr, fieldExists := a.Attr("last_modified")
		if !fieldExists {
			dateStr, _ = a.Attr("add_date")
		}

		// On a rerun, entries that were already handled are not counted.
		if !filter.allows(rawLine, url) {
			return
		}

		result.Total++

		// Using now as default date in case no last_modified nor add_date are present
		modifiedDate := time.Now()
		if dateStr != "" {
			modifiedTsInt, err := strconv.Atoi(dateStr)
			if err != nil {
				skip(&result, model.SkipReasonInvalidDate, err.Error(), url, rawLine)
				return
			}

			modifiedDate = time.Unix(int64(modifiedTsInt), 0)
		}

		// Clean up URL
		cleanURL, err := core.RemoveUTMParams(url)
		if err != nil {
			skip(&result, model.SkipReasonInvalidURL, err.Error(), url, rawLine)
			return
		}
		url = cleanURL

		// Make sure title is valid Utf-8
		title = validateTitle(title, url)

		// Check if the URL already exists before, both in this file or in database
		if _, exist := seenURLs[url]; exist {
			skip(&result, model.SkipReasonDuplicate, "", url, rawLine)
			return
		}

		_, exist, err := db.GetBookmark(ctx, 0, url)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			skip(&result, model.SkipReasonDBError, err.Error(), url, rawLine)
			return
		}

		if exist {
			skip(&result, model.SkipReasonDuplicate, "", url, rawLine)
			seenURLs[url] = struct{}{}
			return
		}

		// Get bookmark tags
		tags := []model.TagDTO{}
		for _, strTag := range strings.Split(strTags, ",") {
			strTag = normalizeSpace(strTag)
			if strTag != "" {
				tags = append(tags, model.TagDTO{
					Tag: model.Tag{Name: strTag},
				})
			}
		}

		// Get category name for this bookmark and add it as tag (if necessary)
		category := normalizeSpace(h3.Text())
		if category != "" && generateTag {
			tags = append(tags, model.TagDTO{
				Tag: model.Tag{Name: category},
			})
		}

		bookmark := model.BookmarkDTO{
			Bookmark: model.Bookmark{
				URL:        url,
				Title:      title,
				ModifiedAt: modifiedDate.Format(model.DatabaseDateFormat),
			},
			Tags: tags,
		}

		seenURLs[url] = struct{}{}
		entries = append(entries, parsedImportEntry{Bookmark: bookmark, RawLine: rawLine})
	})

	return entries, result, nil
}

// executeImport runs the common import pipeline: rerun lookup, parsing,
// saving, persisting the run (including every skipped raw line) and
// printing the structured summary. The parse callback receives the rerun
// filter so it can skip entries already handled by a previous run.
func executeImport(
	ctx context.Context,
	db model.DB,
	source, kind string,
	parse func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error),
) (model.ImportRun, []model.BookmarkDTO, error) {
	startedAt := time.Now().UTC().Format(model.DatabaseDateFormat)

	filter := newRerunFilter()
	if err := filter.loadLatestSkipped(ctx, db, source); err != nil {
		return model.ImportRun{}, nil, err
	}

	entries, result, err := parse(filter)
	if err != nil {
		return model.ImportRun{}, nil, err
	}

	bookmarks := make([]model.BookmarkDTO, 0, len(entries))
	for _, entry := range entries {
		bookmarks = append(bookmarks, entry.Bookmark)
	}

	run := model.ImportRun{
		Source:    source,
		Kind:      kind,
		Status:    model.ImportStatusCompleted,
		Total:     result.Total,
		StartedAt: startedAt,
	}

	saved, err := db.SaveBookmarks(ctx, true, bookmarks...)
	if err != nil {
		// The save is transactional: nothing was persisted. Every
		// candidate is recorded as not successfully imported so that the
		// next run picks it up again.
		run.Status = model.ImportStatusFailed
		for _, entry := range entries {
			skip(&result, model.SkipReasonSaveError, err.Error(), entry.Bookmark.URL, entry.RawLine)
		}
		saved = nil
	} else {
		result.Imported = len(saved)
	}

	run.Imported = result.Imported
	run.Skipped = result.Skipped
	run.FinishedAt = time.Now().UTC().Format(model.DatabaseDateFormat)

	savedRun, saveRunErr := db.SaveImportRun(ctx, run)
	if saveRunErr != nil {
		return run, saved, fmt.Errorf("failed to record import run: %w", saveRunErr)
	}

	printImportResult(savedRun)
	return savedRun, saved, nil
}

// printImportResult prints the structured outcome of an import run.
func printImportResult(run model.ImportRun) {
	fmt.Println()
	cInfo.Println("Import finished")
	cInfo.Printf("  Run ID:        %d\n", run.ID)
	cInfo.Printf("  Source:        %s\n", run.Source)
	cInfo.Printf("  Status:        %s\n", run.Status)
	cInfo.Printf("  Total entries: %d\n", run.Total)
	cInfo.Printf("  Imported:      %d\n", run.Imported)
	cInfo.Printf("  Skipped:       %d\n", len(run.Skipped))

	if len(run.Skipped) == 0 {
		return
	}

	cInfo.Println("  Skipped by reason:")
	grouped := map[string][]model.SkippedItem{}
	for _, item := range run.Skipped {
		grouped[item.Reason] = append(grouped[item.Reason], item)
	}

	// Stable ordering of the reason groups.
	for _, reason := range []string{
		model.SkipReasonInvalidDate,
		model.SkipReasonInvalidURL,
		model.SkipReasonDuplicate,
		model.SkipReasonDBError,
		model.SkipReasonSaveError,
	} {
		items, ok := grouped[reason]
		if !ok {
			continue
		}

		cInfo.Printf("    - %s: %d\n", skipReasonLabel(reason, ""), len(items))
		for _, item := range items {
			cError.Printf("        * %s\n", fallbackText(item.URL, item.RawLine))
		}
	}

	cInfo.Println("  The original lines of every skipped entry were stored in the `runs` table (skipped field).")
}
