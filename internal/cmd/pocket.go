package cmd

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/spf13/cobra"
)

// Sentinel errors used to classify why a Pocket entry was skipped.
var (
	errPocketInvalidURL  = errors.New("URL is not valid")
	errPocketInvalidDate = errors.New("invalid time added")
	errPocketDuplicate   = errors.New("URL already exists")
)

func pocketCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pocket source-file",
		Short: "Import bookmarks from Pocket's data export file",
		Args:  cobra.ExactArgs(1),
		Run:   pocketHandler,
	}

	return cmd
}

func pocketHandler(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()
	_, deps := initShiori(ctx, cmd)

	// Open pocket's file
	filePath := args[0]
	srcFile, err := os.Open(filePath)
	if err != nil {
		cError.Println(err)
		os.Exit(1)
	}
	defer srcFile.Close()

	db := deps.Database()

	var kind string
	var parse func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error)
	switch filepath.Ext(filePath) {
	case ".html":
		kind = model.ImportKindPocket + "_html"
		parse = func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
			return parsePocketHTMLExport(ctx, db, srcFile, filter)
		}
	case ".csv":
		kind = model.ImportKindPocket + "_csv"
		parse = func(filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
			return parsePocketCSVExport(ctx, db, srcFile, filter)
		}
	default:
		cError.Println("Invalid file format. Only HTML and CSV are supported.")
		os.Exit(1)
	}

	// On a rerun only the entries that were not successfully imported in
	// the previous run of the same source are processed.
	_, saved, err := executeImport(ctx, db, filePath, kind, parse)
	if err != nil {
		cError.Printf("Import failed: %v\n", err)
		os.Exit(1)
	}

	// Print imported bookmarks
	fmt.Println()
	printBookmarks(saved...)
}

// parsePocketHTMLExport parses bookmarks from a Pocket HTML export file,
// collecting structured information about every skipped entry.
func parsePocketHTMLExport(ctx context.Context, db model.DB, srcFile *os.File, filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
	result := model.ImportResult{}
	entries := []parsedImportEntry{}
	seenURLs := map[string]struct{}{}

	doc, err := goquery.NewDocumentFromReader(srcFile)
	if err != nil {
		return nil, result, fmt.Errorf("failed to parse bookmark: %w", err)
	}

	doc.Find("a").Each(func(_ int, a *goquery.Selection) {
		rawLine, _ := goquery.OuterHtml(a)
		rawLine = strings.TrimSpace(rawLine)

		title := a.Text()
		url, _ := a.Attr("href")
		tagsStr, _ := a.Attr("tags")
		timeAddedStr, _ := a.Attr("time_added")

		if !filter.allows(rawLine, url) {
			return
		}

		result.Total++

		bookmark, err := buildPocketBookmark(title, url, timeAddedStr, tagsStr)
		if err != nil {
			classifyPocketError(&result, err, url, rawLine)
			return
		}

		if err = checkPocketDuplicate(ctx, db, seenURLs, bookmark.URL); err != nil {
			classifyPocketError(&result, err, bookmark.URL, rawLine)
			return
		}

		seenURLs[bookmark.URL] = struct{}{}
		entries = append(entries, parsedImportEntry{Bookmark: bookmark, RawLine: rawLine})
	})

	return entries, result, nil
}

// parsePocketCSVExport parses bookmarks from a Pocket CSV export file,
// collecting structured information about every skipped entry.
func parsePocketCSVExport(ctx context.Context, db model.DB, srcFile *os.File, filter *rerunFilter) ([]parsedImportEntry, model.ImportResult, error) {
	result := model.ImportResult{}
	entries := []parsedImportEntry{}
	seenURLs := map[string]struct{}{}

	reader := csv.NewReader(srcFile)
	records, err := reader.ReadAll()
	if err != nil {
		return nil, result, fmt.Errorf("failed to read CSV export: %w", err)
	}

	var titleIdx, urlIdx, timeAddedIdx, tagsIdx int
	for i, cols := range records {
		// Check and skip header
		if i == 0 {
			titleIdx = slices.Index(cols, "title")
			urlIdx = slices.Index(cols, "url")
			timeAddedIdx = slices.Index(cols, "time_added")
			tagsIdx = slices.Index(cols, "tags")
			if titleIdx == -1 || urlIdx == -1 || timeAddedIdx == -1 || tagsIdx == -1 {
				return nil, result, errors.New("invalid CSV format. Header must contain: title, url, time_added, tags")
			}
			continue
		}

		// The original CSV row, re-encoded with the same quoting rules the
		// standard library uses, is stored verbatim for skipped entries.
		var rawLineBuilder strings.Builder
		csvWriter := csv.NewWriter(&rawLineBuilder)
		if err := csvWriter.Write(cols); err != nil {
			return nil, result, fmt.Errorf("failed to reconstruct CSV row: %w", err)
		}
		csvWriter.Flush()
		if err := csvWriter.Error(); err != nil {
			return nil, result, fmt.Errorf("failed to reconstruct CSV row: %w", err)
		}
		rawLine := strings.TrimRight(rawLineBuilder.String(), "\r\n")
		url := cols[urlIdx]

		if !filter.allows(rawLine, url) {
			continue
		}

		result.Total++

		bookmark, err := buildPocketBookmark(cols[titleIdx], cols[urlIdx], cols[timeAddedIdx], cols[tagsIdx])
		if err != nil {
			classifyPocketError(&result, err, url, rawLine)
			continue
		}

		if err = checkPocketDuplicate(ctx, db, seenURLs, bookmark.URL); err != nil {
			classifyPocketError(&result, err, bookmark.URL, rawLine)
			continue
		}

		seenURLs[bookmark.URL] = struct{}{}
		entries = append(entries, parsedImportEntry{Bookmark: bookmark, RawLine: rawLine})
	}

	return entries, result, nil
}

// classifyPocketError maps a parsing or duplicate error to a skip reason.
func classifyPocketError(result *model.ImportResult, err error, url, rawLine string) {
	switch {
	case errors.Is(err, errPocketInvalidURL):
		skip(result, model.SkipReasonInvalidURL, err.Error(), url, rawLine)
	case errors.Is(err, errPocketInvalidDate):
		skip(result, model.SkipReasonInvalidDate, err.Error(), url, rawLine)
	case errors.Is(err, errPocketDuplicate):
		skip(result, model.SkipReasonDuplicate, err.Error(), url, rawLine)
	default:
		skip(result, model.SkipReasonDBError, err.Error(), url, rawLine)
	}
}

// buildPocketBookmark validates and assembles a Pocket entry into a
// bookmark DTO.
func buildPocketBookmark(title, url, timeAddedStr, tags string) (model.BookmarkDTO, error) {
	// Clean up URL
	cleanURL, err := core.RemoveUTMParams(url)
	if err != nil {
		return model.BookmarkDTO{}, fmt.Errorf("%w, %s", errPocketInvalidURL, err)
	}
	url = cleanURL

	// Make sure title is valid Utf-8
	title = validateTitle(title, url)

	// Parse time added
	timeAddedInt, err := strconv.ParseInt(timeAddedStr, 10, 64)
	if err != nil {
		return model.BookmarkDTO{}, fmt.Errorf("%w, %s", errPocketInvalidDate, err)
	}
	timeAdded := time.Unix(timeAddedInt, 0)

	// Get bookmark tags. We need to split tags by both comma or pipe,
	// because Pocket's CSV export use pipe as separator, while the HTML
	// export use comma.
	tagsList := []model.TagDTO{}
	for _, tag := range regexp.MustCompile(`[,|]`).Split(tags, -1) {
		if tag != "" {
			tagsList = append(tagsList, model.TagDTO{
				Tag: model.Tag{Name: tag},
			})
		}
	}

	return model.BookmarkDTO{
		Bookmark: model.Bookmark{
			URL:        url,
			Title:      title,
			ModifiedAt: timeAdded.Format(model.DatabaseDateFormat),
			CreatedAt:  timeAdded.Format(model.DatabaseDateFormat),
		},
		Tags: tagsList,
	}, nil
}

// checkPocketDuplicate verifies whether the URL already exists, either
// earlier in the same import file or in the database.
func checkPocketDuplicate(ctx context.Context, db model.DB, seenURLs map[string]struct{}, url string) error {
	if _, exists := seenURLs[url]; exists {
		return errPocketDuplicate
	}

	_, exists, err := db.GetBookmark(ctx, 0, url)
	if err != nil {
		return fmt.Errorf("failed getting bookmark, %w", err)
	}

	if exists {
		return errPocketDuplicate
	}

	return nil
}

// parseCsvExport keeps the legacy entry point used by tests. It discards
// the structured result of the new parser.
func parseCsvExport(ctx context.Context, db model.DB, srcFile *os.File) []model.BookmarkDTO {
	entries, _, err := parsePocketCSVExport(ctx, db, srcFile, newRerunFilter())
	if err != nil {
		cError.Println(err)
		os.Exit(1)
	}

	bookmarks := make([]model.BookmarkDTO, 0, len(entries))
	for _, entry := range entries {
		bookmarks = append(bookmarks, entry.Bookmark)
	}
	return bookmarks
}
