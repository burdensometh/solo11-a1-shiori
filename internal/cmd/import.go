package cmd

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/spf13/cobra"
)

// sniffSize is the number of bytes read from the start of the file
// to detect the bookmark export format.
const sniffSize = 4096

// bookmarkFormat is a bookmark export format detected from file content.
type bookmarkFormat int

const (
	formatUnknown bookmarkFormat = iota
	formatNetscapeHTML
	formatChromeJSON
	formatPocketCSV
)

func (f bookmarkFormat) String() string {
	switch f {
	case formatNetscapeHTML:
		return "Netscape HTML"
	case formatChromeJSON:
		return "Chrome JSON"
	case formatPocketCSV:
		return "Pocket CSV"
	default:
		return "unknown"
	}
}

func importCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "import source-file",
		Short: "Import bookmarks from a Netscape HTML, Chrome JSON or Pocket CSV export file",
		Args:  cobra.ExactArgs(1),
		Run:   importHandler,
	}

	cmd.Flags().BoolP("generate-tag", "t", false, "Auto generate tag from bookmark's category")

	return cmd
}

func importHandler(cmd *cobra.Command, args []string) {
	ctx := cmd.Context()
	_, deps := initShiori(ctx, cmd)

	// Parse flags
	generateTag := cmd.Flags().Changed("generate-tag")

	// If user doesn't specify, ask if tag need to be generated
	if !generateTag {
		var submit string
		fmt.Print("Add parents folder as tag? (y/N): ")
		fmt.Scanln(&submit)

		generateTag = submit == "y"
	}

	// Parse bookmark's file, detecting its format from the content
	bookmarks, err := parseBookmarkFile(ctx, deps.Database(), args[0], generateTag)
	if err != nil {
		cError.Printf("Failed to import %s: %v\n", args[0], err)
		os.Exit(1)
	}

	// Save bookmark to database
	bookmarks, err = deps.Database().SaveBookmarks(ctx, true, bookmarks...)
	if err != nil {
		cError.Printf("Failed to save bookmarks: %v\n", err)
		os.Exit(1)
	}

	// Print imported bookmark
	fmt.Println()
	printBookmarks(bookmarks...)
}

// parseBookmarkFile detects the format of a bookmark export file by sniffing
// its first bytes, then dispatches to the matching parser.
func parseBookmarkFile(ctx context.Context, db model.DB, path string, generateTag bool) ([]model.BookmarkDTO, error) {
	srcFile, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer srcFile.Close()

	// Read the first bytes of the file to sniff its format
	head := make([]byte, sniffSize)
	n, err := io.ReadFull(srcFile, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}
	head = head[:n]

	format := sniffBookmarkFormat(head)

	// Rewind so the parser can read the file from the start
	if _, err := srcFile.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	switch format {
	case formatNetscapeHTML:
		return parseNetscapeBookmarks(ctx, db, srcFile, generateTag), nil
	case formatChromeJSON:
		return parseChromeBookmarks(ctx, db, srcFile, generateTag), nil
	case formatPocketCSV:
		return parseCsvExport(ctx, db, srcFile), nil
	default:
		return nil, fmt.Errorf("unsupported bookmark file format (detected: %s), file starts with: %s",
			format, headerSnippet(head))
	}
}

// sniffBookmarkFormat inspects the first bytes of a file and reports whether
// it looks like a Netscape HTML, Chrome JSON or Pocket CSV bookmark export.
func sniffBookmarkFormat(head []byte) bookmarkFormat {
	content := strings.TrimSpace(string(head))

	switch {
	case isNetscapeHTML(content):
		return formatNetscapeHTML
	case isChromeJSON(content):
		return formatChromeJSON
	case isPocketCSV(content):
		return formatPocketCSV
	default:
		return formatUnknown
	}
}

// isNetscapeHTML reports whether the content carries the doctype that
// browsers put at the top of Netscape-format bookmark exports.
func isNetscapeHTML(content string) bool {
	return strings.Contains(strings.ToLower(content), "<!doctype netscape-bookmark-file-1")
}

// isChromeJSON reports whether the content looks like the JSON bookmarks
// file found in a Chrome profile: an object with a "roots" entry holding
// the well-known root folders.
func isChromeJSON(content string) bool {
	return strings.HasPrefix(content, "{") &&
		strings.Contains(content, `"roots"`) &&
		strings.Contains(content, `"bookmark_bar"`)
}

// isPocketCSV reports whether the first line of the content is a CSV header
// with the columns Pocket uses in its CSV export.
func isPocketCSV(content string) bool {
	firstLine, _, _ := strings.Cut(content, "\n")
	cols, err := csv.NewReader(strings.NewReader(firstLine)).Read()
	if err != nil {
		return false
	}

	found := map[string]bool{"title": false, "url": false, "time_added": false}
	for _, col := range cols {
		col = strings.ToLower(strings.TrimSpace(col))
		if _, ok := found[col]; ok {
			found[col] = true
		}
	}

	for _, ok := range found {
		if !ok {
			return false
		}
	}
	return true
}

// headerSnippet returns a short, quoted preview of the file start,
// for use in error messages.
func headerSnippet(head []byte) string {
	const maxLen = 80
	snippet := strings.TrimSpace(string(head))
	if len(snippet) > maxLen {
		snippet = snippet[:maxLen] + "..."
	}
	return strconv.Quote(snippet)
}

// parseNetscapeBookmarks parses a bookmark export in Netscape Bookmark
// format, as produced by Firefox and most other browsers.
func parseNetscapeBookmarks(ctx context.Context, db model.DB, srcFile io.Reader, generateTag bool) []model.BookmarkDTO {
	bookmarks := []model.BookmarkDTO{}
	mapURL := make(map[string]struct{})

	doc, err := goquery.NewDocumentFromReader(srcFile)
	if err != nil {
		cError.Printf("Failed to parse bookmark: %v\n", err)
		os.Exit(1)
	}

	doc.Find("dt>a").Each(func(_ int, a *goquery.Selection) {
		// Get related elements
		dt := a.Parent()
		dl := dt.Parent()
		h3 := dl.Parent().Find("h3").First()

		// Get metadata
		title := a.Text()
		url, _ := a.Attr("href")
		strTags, _ := a.Attr("tags")

		dateStr, fieldExists := a.Attr("last_modified")
		if !fieldExists {
			dateStr, _ = a.Attr("add_date")
		}

		// Using now as default date in case no last_modified nor add_date are present
		modifiedDate := time.Now()
		if dateStr != "" {
			modifiedTsInt, err := strconv.Atoi(dateStr)
			if err != nil {
				cError.Printf("Skip %s: date field is not valid: %s", url, err)
				return
			}

			modifiedDate = time.Unix(int64(modifiedTsInt), 0)
		}

		// Clean up URL
		url, err = core.RemoveUTMParams(url)
		if err != nil {
			cError.Printf("Skip %s: URL is not valid\n", url)
			return
		}

		// Make sure title is valid Utf-8
		title = validateTitle(title, url)

		// Check if the URL already exist before, both in bookmark
		// file or in database
		if _, exist := mapURL[url]; exist {
			cError.Printf("Skip %s: URL already exists\n", url)
			return
		}

		_, exist, err := db.GetBookmark(ctx, 0, url)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			cError.Printf("Skip %s: Get Bookmark fail, %v", url, err)
			return
		}

		if exist {
			cError.Printf("Skip %s: URL already exists\n", url)
			mapURL[url] = struct{}{}
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

		// Get category name for this bookmark
		// and add it as tags (if necessary)
		category := normalizeSpace(h3.Text())
		if category != "" && generateTag {
			tags = append(tags, model.TagDTO{
				Tag: model.Tag{Name: category},
			})
		}

		// Add item to list
		bookmark := model.BookmarkDTO{
			Bookmark: model.Bookmark{
				URL:        url,
				Title:      title,
				ModifiedAt: modifiedDate.Format(model.DatabaseDateFormat),
			},
			Tags: tags,
		}

		mapURL[url] = struct{}{}
		bookmarks = append(bookmarks, bookmark)
	})

	return bookmarks
}

// chromeBookmarkNode is a node in the bookmarks tree of a Chrome profile's
// JSON bookmarks file: either a folder with children or a single bookmark.
type chromeBookmarkNode struct {
	Type      string               `json:"type"`
	Name      string               `json:"name"`
	URL       string               `json:"url"`
	DateAdded string               `json:"date_added"`
	Children  []chromeBookmarkNode `json:"children"`
}

// chromeBookmarksFile mirrors the top level of a Chrome profile's
// JSON bookmarks file.
type chromeBookmarksFile struct {
	Roots map[string]chromeBookmarkNode `json:"roots"`
}

// chromeEpochOffset is the number of seconds between the Chrome timestamp
// epoch (1601-01-01) and the Unix epoch (1970-01-01).
const chromeEpochOffset = 11644473600

// parseChromeBookmarks parses the JSON bookmarks file exported from a
// Chrome profile, walking the bookmark tree recursively. When generateTag
// is set, the immediate parent folder name is added as a tag.
func parseChromeBookmarks(ctx context.Context, db model.DB, srcFile io.Reader, generateTag bool) []model.BookmarkDTO {
	bookmarks := []model.BookmarkDTO{}
	mapURL := make(map[string]struct{})

	var parsed chromeBookmarksFile
	if err := json.NewDecoder(srcFile).Decode(&parsed); err != nil {
		cError.Printf("Failed to parse bookmark: %v\n", err)
		os.Exit(1)
	}

	// Iterate the well-known roots in Chrome's canonical order, then any
	// additional roots sorted by name, so imports are deterministic.
	rootNames := []string{"bookmark_bar", "other", "synced"}
	extraNames := []string{}
	for name := range parsed.Roots {
		if !slices.Contains(rootNames, name) {
			extraNames = append(extraNames, name)
		}
	}
	sort.Strings(extraNames)
	rootNames = append(rootNames, extraNames...)

	var walk func(node *chromeBookmarkNode, parentFolder string)
	walk = func(node *chromeBookmarkNode, parentFolder string) {
		if node.Type == "folder" {
			for i := range node.Children {
				walk(&node.Children[i], node.Name)
			}
			return
		}

		if node.Type != "url" {
			return
		}

		// Clean up URL
		url, err := core.RemoveUTMParams(node.URL)
		if err != nil {
			cError.Printf("Skip %s: URL is not valid\n", node.URL)
			return
		}

		// Make sure title is valid Utf-8
		title := validateTitle(node.Name, url)

		// Using now as default date in case date_added is missing
		modifiedDate := time.Now()
		if node.DateAdded != "" && node.DateAdded != "0" {
			chromeTsInt, err := strconv.ParseInt(node.DateAdded, 10, 64)
			if err != nil {
				cError.Printf("Skip %s: date field is not valid: %s", url, err)
				return
			}

			// Chrome timestamps are microseconds since 1601-01-01
			modifiedDate = time.Unix(chromeTsInt/1_000_000-chromeEpochOffset, chromeTsInt%1_000_000*1000)
		}

		// Check if the URL already exist before, both in bookmark
		// file or in database
		if err := handleDuplicates(ctx, db, mapURL, url); err != nil {
			cError.Printf("Skip %s: %v\n", url, err)
			return
		}

		// Add the parent folder name as tag (if necessary)
		tags := []model.TagDTO{}
		if parentFolder != "" && generateTag {
			tags = append(tags, model.TagDTO{
				Tag: model.Tag{Name: parentFolder},
			})
		}

		// Add item to list
		bookmark := model.BookmarkDTO{
			Bookmark: model.Bookmark{
				URL:        url,
				Title:      title,
				ModifiedAt: modifiedDate.Format(model.DatabaseDateFormat),
			},
			Tags: tags,
		}

		mapURL[url] = struct{}{}
		bookmarks = append(bookmarks, bookmark)
	}

	for _, rootName := range rootNames {
		root, ok := parsed.Roots[rootName]
		if !ok {
			continue
		}
		// The roots themselves (Bookmarks bar, ...) are structural,
		// so their children start without a parent folder
		for i := range root.Children {
			walk(&root.Children[i], "")
		}
	}

	return bookmarks
}
