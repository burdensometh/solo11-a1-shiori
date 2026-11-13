package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-shiori/shiori/internal/database"
	"github.com/go-shiori/shiori/internal/model"
)

func newTestDB(t *testing.T) model.DB {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "shiori.db")
	db, err := database.OpenSQLiteDatabase(context.TODO(), dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite database: %v", err)
	}

	if err := db.Migrate(context.TODO()); err != nil {
		t.Fatalf("failed to migrate sqlite database: %v", err)
	}

	return db
}

func readHead(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	if len(content) > sniffSize {
		content = content[:sniffSize]
	}
	return content
}

func Test_sniffBookmarkFormat(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		expected bookmarkFormat
	}{
		{
			name:     "Netscape HTML doctype",
			content:  "<!DOCTYPE NETSCAPE-Bookmark-file-1>\n<DL><p>\n<DT><A HREF=\"https://example.com\">Example</A>\n</DL><p>",
			expected: formatNetscapeHTML,
		},
		{
			name:     "Netscape HTML doctype lowercase",
			content:  "<!doctype netscape-bookmark-file-1>\n<DL><p></DL><p>",
			expected: formatNetscapeHTML,
		},
		{
			name:     "Chrome JSON",
			content:  "{\n  \"checksum\": \"abc\",\n  \"roots\": {\n    \"bookmark_bar\": {},\n    \"other\": {},\n    \"synced\": {}\n  },\n  \"version\": 1\n}",
			expected: formatChromeJSON,
		},
		{
			name:     "Pocket CSV old header",
			content:  "title,url,time_added,tags,status\nShiori,https://github.com/go-shiori/shiori,1541343937,shiori,unread\n",
			expected: formatPocketCSV,
		},
		{
			name:     "Pocket CSV new header",
			content:  "title,url,time_added,cursor,tags,status\nShiori,https://github.com/go-shiori/shiori,1541343937,,shiori,unread\n",
			expected: formatPocketCSV,
		},
		{
			name:     "leading whitespace before JSON",
			content:  "  \n{\n  \"roots\": {\"bookmark_bar\": {}}\n}",
			expected: formatChromeJSON,
		},
		{
			name:     "generic JSON is unknown",
			content:  "{\"foo\": \"bar\"}",
			expected: formatUnknown,
		},
		{
			name:     "generic HTML is unknown",
			content:  "<!DOCTYPE html>\n<html><body><a href=\"https://example.com\">Example</a></body></html>",
			expected: formatUnknown,
		},
		{
			name:     "plain text is unknown",
			content:  "this is not a bookmark export file",
			expected: formatUnknown,
		},
		{
			name:     "empty file is unknown",
			content:  "",
			expected: formatUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sniffBookmarkFormat([]byte(tt.content)); got != tt.expected {
				t.Errorf("expected format %s, got %s", tt.expected, got)
			}
		})
	}
}

func Test_sniffBookmarkFormat_testdata(t *testing.T) {
	tests := []struct {
		fileName string
		expected bookmarkFormat
	}{
		{"bookmarks-netscape.html", formatNetscapeHTML},
		{"bookmarks-chrome.json", formatChromeJSON},
		{"pocket-old.csv", formatPocketCSV},
		{"pocket-new.csv", formatPocketCSV},
	}

	for _, tt := range tests {
		t.Run(tt.fileName, func(t *testing.T) {
			head := readHead(t, filepath.Join("../../testdata", tt.fileName))
			if got := sniffBookmarkFormat(head); got != tt.expected {
				t.Errorf("expected format %s, got %s", tt.expected, got)
			}
		})
	}
}

func Test_parseBookmarkFile_netscape(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	bookmarks, err := parseBookmarkFile(ctx, db, "../../testdata/bookmarks-netscape.html", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bookmarks) != 3 {
		t.Fatalf("expected 3 bookmarks, got %d", len(bookmarks))
	}

	byURL := map[string]model.BookmarkDTO{}
	for _, bm := range bookmarks {
		byURL[bm.URL] = bm
	}

	shiori, ok := byURL["https://github.com/go-shiori/shiori"]
	if !ok {
		t.Fatal("expected bookmark for https://github.com/go-shiori/shiori")
	}
	if shiori.Title != "Shiori" {
		t.Errorf("expected title Shiori, got %s", shiori.Title)
	}
	assertTagNames(t, shiori, []string{"shiori", "bookmark", "Dev"})

	golang, ok := byURL["https://go.dev"]
	if !ok {
		t.Fatal("expected bookmark for https://go.dev")
	}
	assertTagNames(t, golang, []string{"Dev"})
	if golang.ModifiedAt != time.Unix(1541343940, 0).Format(model.DatabaseDateFormat) {
		t.Errorf("unexpected modified date: %s", golang.ModifiedAt)
	}

	if _, ok := byURL["https://example.com"]; !ok {
		t.Fatal("expected bookmark for https://example.com")
	}
}

func Test_parseBookmarkFile_netscape_no_tags(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	bookmarks, err := parseBookmarkFile(ctx, db, "../../testdata/bookmarks-netscape.html", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, bm := range bookmarks {
		for _, tag := range bm.Tags {
			if tag.Name == "Dev" {
				t.Errorf("expected no generated folder tag for %s, got %v", bm.URL, bm.Tags)
			}
		}
	}
}

func Test_parseBookmarkFile_chrome(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	bookmarks, err := parseBookmarkFile(ctx, db, "../../testdata/bookmarks-chrome.json", true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bookmarks) != 3 {
		t.Fatalf("expected 3 bookmarks, got %d", len(bookmarks))
	}

	byURL := map[string]model.BookmarkDTO{}
	for _, bm := range bookmarks {
		byURL[bm.URL] = bm
	}

	// Bookmark at the root of the bookmarks bar gets no folder tag
	shiori, ok := byURL["https://github.com/go-shiori/shiori"]
	if !ok {
		t.Fatal("expected bookmark for https://github.com/go-shiori/shiori")
	}
	if shiori.Title != "Shiori" {
		t.Errorf("expected title Shiori, got %s", shiori.Title)
	}
	assertTagNames(t, shiori, nil)

	// Chrome timestamps are microseconds since 1601-01-01:
	// 13251268937000000 us -> 1606795337 unix
	expectedDate := time.Unix(1606795337, 0).Format(model.DatabaseDateFormat)
	if shiori.ModifiedAt != expectedDate {
		t.Errorf("expected modified date %s, got %s", expectedDate, shiori.ModifiedAt)
	}

	// Bookmark inside the "Dev" folder gets the folder name as tag
	golang, ok := byURL["https://go.dev"]
	if !ok {
		t.Fatal("expected bookmark for https://go.dev")
	}
	assertTagNames(t, golang, []string{"Dev"})

	// Bookmark in the "other" root is imported as well
	if _, ok := byURL["https://example.com"]; !ok {
		t.Fatal("expected bookmark for https://example.com")
	}
}

func Test_parseBookmarkFile_chrome_no_tags(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	bookmarks, err := parseBookmarkFile(ctx, db, "../../testdata/bookmarks-chrome.json", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bookmarks) != 3 {
		t.Fatalf("expected 3 bookmarks, got %d", len(bookmarks))
	}

	for _, bm := range bookmarks {
		if len(bm.Tags) != 0 {
			t.Errorf("expected no tags for %s, got %v", bm.URL, bm.Tags)
		}
	}
}

func Test_parseBookmarkFile_pocketCSV(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	bookmarks, err := parseBookmarkFile(ctx, db, "../../testdata/pocket-new.csv", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(bookmarks) != 1 {
		t.Fatalf("expected 1 bookmark, got %d", len(bookmarks))
	}

	bm := bookmarks[0]
	if bm.Title != "Shiori" {
		t.Errorf("expected title Shiori, got %s", bm.Title)
	}
	if bm.URL != "https://github.com/go-shiori/shiori" {
		t.Errorf("expected URL https://github.com/go-shiori/shiori, got %s", bm.URL)
	}
	assertTagNames(t, bm, []string{"shiori"})
}

func Test_parseBookmarkFile_unknown(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	path := filepath.Join(t.TempDir(), "bookmarks.txt")
	content := "this is not a bookmark export file"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := parseBookmarkFile(ctx, db, path, false)
	if err == nil {
		t.Fatal("expected an error for unknown format, got nil")
	}

	if !strings.Contains(err.Error(), "detected: unknown") {
		t.Errorf("expected error to report detected type, got: %v", err)
	}
	if !strings.Contains(err.Error(), `"`+content+`"`) {
		t.Errorf("expected error to contain the file header, got: %v", err)
	}
}

func Test_parseBookmarkFile_missing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.TODO()

	_, err := parseBookmarkFile(ctx, db, filepath.Join(t.TempDir(), "does-not-exist.html"), false)
	if err == nil {
		t.Fatal("expected an error for missing file, got nil")
	}
	if !strings.Contains(err.Error(), "failed to open file") {
		t.Errorf("expected a clear open error, got: %v", err)
	}
}

func assertTagNames(t *testing.T, bm model.BookmarkDTO, expected []string) {
	t.Helper()

	names := []string{}
	for _, tag := range bm.Tags {
		names = append(names, tag.Name)
	}

	if len(names) != len(expected) {
		t.Errorf("expected tags %v for %s, got %v", expected, bm.URL, names)
		return
	}
	for i := range expected {
		if names[i] != expected[i] {
			t.Errorf("expected tags %v for %s, got %v", expected, bm.URL, names)
			return
		}
	}
}
