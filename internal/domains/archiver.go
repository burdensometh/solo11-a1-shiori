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

	processRequest := core.ProcessRequest{
		DataDir:     d.deps.Config().Storage.DataDir,
		Bookmark:    book,
		Content:     content,
		ContentType: contentType,
	}

	result, isFatalErr, err := core.ProcessBookmark(d.deps, processRequest)
	content.Close()

	if err != nil && isFatalErr {
		return nil, fmt.Errorf("failed to process: %v", err)
	}

	return &result, nil
}

func (d *ArchiverDomain) GetBookmarkArchive(book *model.BookmarkDTO) (*warc.Archive, error) {
	archivePath := model.GetArchivePath(book)

	if !d.deps.Domains().Storage().FileExists(archivePath) {
		return nil, fmt.Errorf("archive for bookmark %d doesn't exist", book.ID)
	}

	// FIXME: This only works in local filesystem
	return warc.Open(filepath.Join(d.deps.Config().Storage.DataDir, archivePath))
}

// GetArchiveManifest returns the resource manifest of a bookmark archive.
func (d *ArchiverDomain) GetArchiveManifest(book *model.BookmarkDTO) (*model.ArchiveManifest, error) {
	return core.LoadArchiveManifest(d.deps, book)
}

// RetryArchiveResources downloads again the missing resources of a bookmark
// archive (optionally limited to the submitted URLs) and updates its
// resource manifest.
func (d *ArchiverDomain) RetryArchiveResources(book *model.BookmarkDTO, urls []string) (*model.ArchiveManifest, error) {
	return core.RetryArchiveResources(d.deps, book, urls)
}

func NewArchiverDomain(deps *dependencies.Dependencies) *ArchiverDomain {
	return &ArchiverDomain{
		deps: deps,
	}
}
