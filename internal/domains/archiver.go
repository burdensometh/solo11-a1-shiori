package domains

import (
	"fmt"
	"path/filepath"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/dependencies"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/warc"
)

type ArchiverDomain struct {
	deps *dependencies.Dependencies
}

func (d *ArchiverDomain) DownloadBookmarkArchive(book model.BookmarkDTO) (*model.BookmarkDTO, error) {
	content, contentType, err := core.DownloadBookmark(book.URL)
	if err != nil {
		return nil, fmt.Errorf("error downloading url: %s", err)
	}
	defer content.Close()

	processRequest := core.ProcessRequest{
		DataDir:     d.deps.Config().Storage.DataDir,
		Bookmark:    book,
		Content:     content,
		ContentType: contentType,
	}

	result, isFatalErr, err := core.ProcessBookmark(d.deps, processRequest)
	if err != nil && isFatalErr {
		return nil, fmt.Errorf("failed to process: %v", err)
	}

	// Non-fatal errors, such as a missing thumbnail, still leave a usable archive.
	return &result, nil
}

// HasArchive reports whether a complete archive for the bookmark URL is stored.
func (d *ArchiverDomain) HasArchive(book *model.BookmarkDTO) bool {
	return core.ArchiveExists(d.deps, book)
}

func (d *ArchiverDomain) GetBookmarkArchive(book *model.BookmarkDTO) (*warc.Archive, error) {
	archivePath, exists := core.FindArchive(d.deps.Domains().Storage(), book)
	if !exists {
		return nil, fmt.Errorf("archive for bookmark %d doesn't exist", book.ID)
	}

	// FIXME: This only works in local filesystem
	return warc.Open(filepath.Join(d.deps.Config().Storage.DataDir, archivePath))
}

// DeleteArchive removes the archive stored for the bookmark. Archives are
// shared by URL, so this is only called when no other bookmark references the
// same URL.
func (d *ArchiverDomain) DeleteArchive(book *model.BookmarkDTO) error {
	archiveDir := model.GetArchiveDirPath(book)

	if !d.deps.Domains().Storage().DirExists(archiveDir) {
		return nil
	}

	return d.deps.Domains().Storage().FS().RemoveAll(archiveDir)
}

func NewArchiverDomain(deps *dependencies.Dependencies) *ArchiverDomain {
	return &ArchiverDomain{
		deps: deps,
	}
}
