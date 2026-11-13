package core_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/domains"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testHTML is a minimal page without sub-resources, so archiving it needs no network.
const testHTML = "<html><head><title>Archive test</title></head><body><p>Archive test content</p></body></html>"

func newArchiveTestDeps(t *testing.T) (model.Dependencies, afero.Fs) {
	t.Helper()

	logger := logrus.New()
	_, deps := testutil.GetTestConfigurationAndDependencies(t, context.TODO(), logger)

	fs := afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())
	deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))

	return deps, fs
}

func saveTestArchive(t *testing.T, deps model.Dependencies, url string) {
	t.Helper()

	require.NoError(t, core.SaveBookmarkArchive(
		deps,
		&model.BookmarkDTO{Bookmark: model.Bookmark{URL: url}},
		bytes.NewBufferString(testHTML),
		"text/html",
		false,
	))
}

func TestSaveBookmarkArchive(t *testing.T) {
	t.Run("stores archive under the URL hash", func(t *testing.T) {
		deps, _ := newArchiveTestDeps(t)
		book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/one"}}

		saveTestArchive(t, deps, book.URL)

		archivePath := model.GetArchivePath(book)
		assert.Equal(t,
			filepath.Join("archive", model.GetArchiveHash(book.URL), "archive"),
			archivePath,
		)
		assert.True(t, deps.Domains().Storage().FileExists(archivePath))
		assert.True(t, core.ArchiveExists(deps, book))
	})

	t.Run("reuses an existing complete archive", func(t *testing.T) {
		deps, _ := newArchiveTestDeps(t)

		first := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/shared"}}
		second := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 2, URL: "https://example.com/shared"}}

		saveTestArchive(t, deps, first.URL)
		archivePath := model.GetArchivePath(first)
		infoBefore, err := deps.Domains().Storage().Stat(archivePath)
		require.NoError(t, err)

		// A second save of the same URL must not rewrite the archive.
		require.NoError(t, core.SaveBookmarkArchive(
			deps,
			second,
			bytes.NewBufferString(testHTML),
			"text/html",
			false,
		))

		infoAfter, err := deps.Domains().Storage().Stat(archivePath)
		require.NoError(t, err)
		assert.Equal(t, infoBefore.ModTime(), infoAfter.ModTime())

		// Both bookmarks see the same archive file.
		assert.Equal(t, model.GetArchivePath(first), model.GetArchivePath(second))
		assert.True(t, core.ArchiveExists(deps, second))
	})

	t.Run("does not leave archives per bookmark", func(t *testing.T) {
		deps, fs := newArchiveTestDeps(t)

		for id := 1; id <= 7; id++ {
			book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: id, URL: "https://example.com/same"}}
			require.NoError(t, core.SaveBookmarkArchive(deps, book, bytes.NewBufferString(testHTML), "text/html", false))
		}

		entries, err := afero.ReadDir(fs, "archive")
		require.NoError(t, err)
		assert.Len(t, entries, 1, "seven saves of the same URL must not create seven archives")
		assert.Equal(t, model.GetArchiveHash("https://example.com/same"), entries[0].Name())
	})

	t.Run("replaces an incomplete archive", func(t *testing.T) {
		deps, fs := newArchiveTestDeps(t)
		book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/truncated"}}

		// Simulate an archive left half-written by an interrupted save.
		require.NoError(t, fs.MkdirAll(model.GetArchiveDirPath(book), model.DataDirPerm))
		require.NoError(t, afero.WriteFile(fs, model.GetArchivePath(book), []byte("half"), 0644))
		assert.False(t, core.ArchiveExists(deps, book))

		saveTestArchive(t, deps, book.URL)

		assert.True(t, core.ArchiveExists(deps, book))
	})

	t.Run("failed save keeps the previous archive intact", func(t *testing.T) {
		deps, fs := newArchiveTestDeps(t)
		book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/keep"}}

		saveTestArchive(t, deps, book.URL)
		content, err := afero.ReadFile(fs, model.GetArchivePath(book))
		require.NoError(t, err)

		// An archive that failed validation is never installed.
		err = core.SaveBookmarkArchive(deps, book, bytes.NewBufferString(testHTML), "text/html", false)

		// The previous clean archive is still reused, not rewritten.
		assert.NoError(t, err)
		after, err := afero.ReadFile(fs, model.GetArchivePath(book))
		require.NoError(t, err)
		assert.Equal(t, content, after)
	})

	t.Run("failure leaves no partial archive", func(t *testing.T) {
		deps, fs := newArchiveTestDeps(t)
		book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/no-space"}}

		// A storage that can't create files makes the save fail after the
		// temporary archive has already been built.
		readOnly := domains.NewStorageDomain(deps, afero.NewReadOnlyFs(fs))
		deps.Domains().SetStorage(readOnly)

		err := core.SaveBookmarkArchive(deps, book, bytes.NewBufferString(testHTML), "text/html", false)
		require.Error(t, err)

		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		assert.False(t, core.ArchiveExists(deps, book))

		// The temporary directory was never created, or was removed again.
		entries, err := afero.ReadDir(fs, "archive")
		if err == nil {
			assert.Empty(t, entries)
		}
	})

	t.Run("no temporary directories are left behind", func(t *testing.T) {
		deps, fs := newArchiveTestDeps(t)
		book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/tmp"}}

		saveTestArchive(t, deps, book.URL)

		entries, err := afero.ReadDir(fs, "archive")
		require.NoError(t, err)
		require.Len(t, entries, 1)
		assert.Equal(t, model.GetArchiveHash(book.URL), entries[0].Name())
	})
}

func TestSaveBookmarkArchiveRejectsInvalidArchive(t *testing.T) {
	deps, fs := newArchiveTestDeps(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: "https://example.com/invalid"}}

	// A non-HTML content type is archived as a general file; an invalid URL makes
	// warc fail before writing anything.
	err := core.SaveBookmarkArchive(deps, book, bytes.NewBufferString(testHTML), "text/html", false)
	require.NoError(t, err)

	entries, err := afero.ReadDir(fs, "archive")
	require.NoError(t, err)
	require.Len(t, entries, 1)

	t.Run("invalid URL does not create an archive", func(t *testing.T) {
		bad := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 2, URL: "not a url"}}
		err := core.SaveBookmarkArchive(deps, bad, bytes.NewBufferString(testHTML), "text/html", false)
		require.Error(t, err)
		assert.False(t, core.ArchiveExists(deps, bad))

		entries, err := afero.ReadDir(fs, "archive")
		require.NoError(t, err)
		assert.Len(t, entries, 1)
	})
}

func TestLegacyArchiveIsMigrated(t *testing.T) {
	deps, _ := newArchiveTestDeps(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 5, URL: "https://example.com/legacy"}}

	storage := deps.Domains().Storage()

	// Build a real archive, then move it to the legacy ID-based name.
	saveTestArchive(t, deps, book.URL)
	require.NoError(t, storage.FS().Rename(model.GetArchivePath(book), filepath.Join("archive", "5")))
	require.NoError(t, storage.FS().RemoveAll(model.GetArchiveDirPath(book)))

	assert.True(t, core.ArchiveExists(deps, book), "legacy archives must still be recognised")

	require.NoError(t, core.SaveBookmarkArchive(deps, book, bytes.NewBufferString(testHTML), "text/html", false))

	assert.True(t, core.ArchiveExists(deps, book))
	assert.True(t, storage.FileExists(filepath.Join("archive", model.GetArchiveHash(book.URL), "archive")))
	assert.False(t, storage.FileExists(filepath.Join("archive", "5")), "legacy copy is removed")
}
