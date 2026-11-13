package core

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/warc"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testStorage is a minimal StorageDomain over an afero filesystem. Tests in
// this package can't import the domains package, since it imports core.
type testStorage struct {
	fs afero.Fs
}

func newTestStorage(t *testing.T) *testStorage {
	t.Helper()
	return &testStorage{fs: afero.NewBasePathFs(afero.NewOsFs(), t.TempDir())}
}

func (s *testStorage) Stat(name string) (fs.FileInfo, error) { return s.fs.Stat(name) }
func (s *testStorage) FS() afero.Fs                          { return s.fs }

func (s *testStorage) FileExists(name string) bool {
	info, err := s.fs.Stat(name)
	return err == nil && !info.IsDir()
}

func (s *testStorage) DirExists(name string) bool {
	info, err := s.fs.Stat(name)
	return err == nil && info.IsDir()
}

func (s *testStorage) WriteData(dst string, data []byte) error {
	return afero.WriteFile(s.fs, dst, data, model.DataDirPerm)
}

func (s *testStorage) WriteFile(dst string, src *os.File) error {
	data, err := os.ReadFile(src.Name())
	if err != nil {
		return err
	}
	return s.WriteData(dst, data)
}

// buildArchiveBytes returns the bytes of a real, complete archive.
func buildArchiveBytes(t *testing.T, url string) []byte {
	t.Helper()

	tmpPath := filepath.Join(t.TempDir(), "archive")
	require.NoError(t, warc.NewArchive(warc.ArchivalRequest{
		URL:         url,
		Reader:      strings.NewReader("<html><body><p>content</p></body></html>"),
		ContentType: "text/html",
	}, tmpPath))

	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err)

	return data
}

func TestInstallArchiveRejectsTruncatedSource(t *testing.T) {
	storage := newTestStorage(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{URL: "https://example.com/staged"}}

	// A staged archive that is cut short must never be renamed into place.
	err := installArchive(storage, book, strings.NewReader("truncated"))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArchive)

	assert.False(t, fileIsValidArchive(storage, model.GetArchivePath(book)))
	assert.False(t, storage.DirExists(model.GetArchiveDirPath(book)))

	// Only the parent directory may exist, never a temporary one.
	entries, err := afero.ReadDir(storage.FS(), "archive")
	if err == nil {
		assert.Empty(t, entries)
	}
}

func TestInstallArchiveLeavesNoTemporaryDirectories(t *testing.T) {
	storage := newTestStorage(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{URL: "https://example.com/temponly"}}

	require.NoError(t, installArchive(storage, book, bytes.NewReader(buildArchiveBytes(t, book.URL))))
	assert.True(t, fileIsValidArchive(storage, model.GetArchivePath(book)))

	entries, err := afero.ReadDir(storage.FS(), "archive")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, model.GetArchiveHash(book.URL), entries[0].Name())
}

func TestInstallArchiveKeepsExistingCompleteArchive(t *testing.T) {
	storage := newTestStorage(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{URL: "https://example.com/first-wins"}}

	require.NoError(t, installArchive(storage, book, bytes.NewReader(buildArchiveBytes(t, book.URL))))
	require.NoError(t, installArchive(storage, book, bytes.NewReader(buildArchiveBytes(t, book.URL))))

	entries, err := afero.ReadDir(storage.FS(), "archive")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, fileIsValidArchive(storage, model.GetArchivePath(book)))
}

func TestInstallArchiveConcurrentSameURL(t *testing.T) {
	storage := newTestStorage(t)
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{URL: "https://example.com/concurrent"}}
	data := buildArchiveBytes(t, book.URL)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			// Both writers stage the same archive; only one directory may win.
			assert.NoError(t, installArchive(storage, book, bytes.NewReader(data)))
		}()
	}
	wg.Wait()

	entries, err := afero.ReadDir(storage.FS(), "archive")
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.True(t, fileIsValidArchive(storage, model.GetArchivePath(book)))
}
