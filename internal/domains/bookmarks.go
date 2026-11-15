package domains

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/database"
	"github.com/go-shiori/shiori/internal/model"
)

type BookmarksDomain struct {
	deps model.Dependencies
}

func (d *BookmarksDomain) HasEbook(b *model.BookmarkDTO) bool {
	ebookPath := model.GetEbookPath(b)
	return d.deps.Domains().Storage().FileExists(ebookPath)
}

func (d *BookmarksDomain) HasArchive(b *model.BookmarkDTO) bool {
	archivePath := model.GetArchivePath(b)
	return d.deps.Domains().Storage().FileExists(archivePath)
}

func (d *BookmarksDomain) HasThumbnail(b *model.BookmarkDTO) bool {
	thumbnailPath := model.GetThumbnailPath(b)
	return d.deps.Domains().Storage().FileExists(thumbnailPath)
}

// populateAdditionalFields sets HasEbook, HasArchive, HasThumbnail, and ImageURL fields on the bookmark
func (d *BookmarksDomain) populateAdditionalFields(bookmark *model.BookmarkDTO) {
	bookmark.HasEbook = d.HasEbook(bookmark)
	bookmark.HasArchive = d.HasArchive(bookmark)
	bookmark.HasThumbnail = d.HasThumbnail(bookmark)
	bookmark.PopulateImageURL()
}

func (d *BookmarksDomain) GetBookmark(ctx context.Context, id model.DBID) (*model.BookmarkDTO, error) {
	if id <= 0 {
		return nil, model.ErrBookmarkNotFound
	}

	bookmark, exists, err := d.deps.Database().GetBookmark(ctx, int(id), "")
	if err != nil {
		return nil, fmt.Errorf("failed to get bookmark: %w", err)
	}

	if !exists {
		return nil, model.ErrBookmarkNotFound
	}

	// Populate additional fields
	d.populateAdditionalFields(&bookmark)

	return &bookmark, nil
}

func (d *BookmarksDomain) GetBookmarks(ctx context.Context, ids []int) ([]model.BookmarkDTO, error) {
	// An empty IDs filter matches every bookmark.
	if len(ids) == 0 {
		return []model.BookmarkDTO{}, nil
	}

	bookmarks, err := d.deps.Database().GetBookmarks(ctx, model.DBGetBookmarksOptions{
		IDs:         ids,
		WithContent: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get bookmarks: %w", err)
	}

	for i := range bookmarks {
		d.populateAdditionalFields(&bookmarks[i])
	}
	return bookmarks, nil
}

func (d *BookmarksDomain) SearchBookmarks(ctx context.Context, options model.BookmarksSearchOptions) ([]model.BookmarkDTO, error) {
	// Convert domain options to database options
	dbOptions := options.ToDBGetBookmarksOptions()

	// Get bookmarks from database with search options (this already includes tags)
	bookmarks, err := d.deps.Database().GetBookmarks(ctx, dbOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to search bookmarks: %w", err)
	}

	// Populate domain-specific fields for each bookmark (ImageURL, HasArchive, HasEbook)
	for i := range bookmarks {
		d.populateAdditionalFields(&bookmarks[i])
	}

	return bookmarks, nil
}

func (d *BookmarksDomain) CountBookmarks(ctx context.Context, options model.BookmarksSearchOptions) (int, error) {
	// Convert domain options to database options (without pagination)
	dbOptions := model.DBGetBookmarksOptions{
		IDs:          options.IDs,
		Tags:         options.Tags,
		ExcludedTags: options.ExcludedTags,
		Keyword:      options.Keyword,
		WithContent:  options.WithContent,
		// Deliberately omit Limit and Offset for counting
	}

	count, err := d.deps.Database().GetBookmarksCount(ctx, dbOptions)
	if err != nil {
		return 0, fmt.Errorf("failed to count bookmarks: %w", err)
	}

	return count, nil
}

func (d *BookmarksDomain) UpdateBookmarkCache(ctx context.Context, bookmark model.BookmarkDTO, keepMetadata bool, skipExist bool) (*model.BookmarkDTO, error) {
	// Download data from internet
	content, contentType, err := core.DownloadBookmark(bookmark.URL)
	if err != nil {
		err = fmt.Errorf("failed to download bookmark: %w", err)
		bookmark.ProcessingStatus = model.BookmarkProcessingFailed
		bookmark.ProcessingFailedStep = model.BookmarkStepDownload
		bookmark.ProcessingError = err.Error()
		// Return the annotated DTO together with the error so callers can persist
		// and display the failed step instead of guessing it.
		return &bookmark, err
	}
	defer content.Close()

	// Check if we should skip existing ebook
	if skipExist && bookmark.CreateEbook {
		ebookPath := model.GetEbookPath(&bookmark)
		if d.deps.Domains().Storage().FileExists(ebookPath) {
			bookmark.CreateEbook = false
			bookmark.HasEbook = true
		}
	}

	// Process the bookmark
	request := core.ProcessRequest{
		DataDir:     d.deps.Config().Storage.DataDir,
		Bookmark:    bookmark,
		Content:     content,
		ContentType: contentType,
		KeepTitle:   keepMetadata,
		KeepExcerpt: keepMetadata,
	}

	processedBookmark, _, err := core.ProcessBookmark(d.deps, request)
	if err != nil {
		// ProcessBookmark already annotated the DTO with the failed step.
		d.populateAdditionalFields(&processedBookmark)
		return &processedBookmark, fmt.Errorf("failed to process bookmark: %w", err)
	}

	d.populateAdditionalFields(&processedBookmark)

	return &processedBookmark, nil
}

// IngestBookmark creates a bookmark or reuses the existing one for the same
// URL, downloads (or uses the provided) page content, processes it and stores
// the result. Processing errors never bubble up as a request failure: the
// bookmark row is still saved and the returned DTO carries a ProcessingStatus
// describing what happened, which lets both the web UI and the extension show
// the failure and retry it later.
func (d *BookmarksDomain) IngestBookmark(ctx context.Context, request model.IngestBookmarkRequest) (*model.BookmarkDTO, error) {
	cleanURL, err := core.RemoveUTMParams(strings.TrimSpace(request.URL))
	if err != nil {
		return nil, fmt.Errorf("failed to clean URL: %w", err)
	}
	request.URL = cleanURL

	userHasDefinedTitle := strings.TrimSpace(request.Title) != ""
	userHasDefinedExcerpt := strings.TrimSpace(request.Excerpt) != ""

	// Fetch existing bookmark for this URL, if any.
	existing, exist, err := d.deps.Database().GetBookmark(ctx, 0, request.URL)
	if err != nil {
		return nil, fmt.Errorf("failed to look up bookmark: %w", err)
	}

	var book model.BookmarkDTO
	if exist {
		if !request.UpdateIfExists {
			return nil, model.ErrBookmarkAlreadyExists
		}

		book = existing
		// Re-adding an existing bookmark merges the incoming tags instead of
		// replacing them.
		knownTags := map[string]struct{}{}
		for _, tag := range book.Tags {
			knownTags[tag.Name] = struct{}{}
		}
		for _, tag := range request.Tags {
			if _, ok := knownTags[tag.Name]; !ok {
				book.Tags = append(book.Tags, tag)
			}
		}
	} else {
		book = model.BookmarkDTO{
			Bookmark: model.Bookmark{
				URL:     request.URL,
				Title:   request.Title,
				Excerpt: request.Excerpt,
				Public:  request.Public,
			},
			Tags: request.Tags,
		}
		if strings.TrimSpace(book.Title) == "" {
			book.Title = book.URL
		}

		// Save first so the bookmark gets an ID, which processing needs in order
		// to write the thumbnail/archive files.
		saved, err := d.deps.Database().SaveBookmarks(ctx, true, book)
		if err != nil {
			if errors.Is(err, database.ErrBookmarkURLAlreadyExists) {
				return nil, model.ErrBookmarkAlreadyExists
			}
			return nil, fmt.Errorf("failed to save bookmark: %w", err)
		}
		book = saved[0]
	}

	// Archive/ebook flags always follow the latest request so retrying with a
	// different configuration takes effect immediately.
	book.CreateArchive = request.CreateArchive
	book.CreateEbook = request.CreateEbook

	// Obtain the page content: the extension may provide the HTML itself.
	var (
		content     io.Reader
		contentType string
	)
	if len(request.Content) > 0 {
		content = bytes.NewReader(request.Content)
		if strings.TrimSpace(request.ContentType) != "" {
			contentType = request.ContentType
		} else {
			contentType = "text/html; charset=UTF-8"
		}
	} else {
		body, downloadType, downloadErr := core.DownloadBookmark(book.URL)
		if downloadErr != nil {
			err = fmt.Errorf("failed to download bookmark: %w", downloadErr)
			book.ProcessingStatus = model.BookmarkProcessingFailed
			book.ProcessingFailedStep = model.BookmarkStepDownload
			book.ProcessingError = err.Error()
			d.deps.Logger().WithError(downloadErr).WithField("url", book.URL).Warn("bookmark download failed")

			// Persist the failure so it stays visible after a page refresh and
			// can be cleared by a later retry.
			if _, saveErr := d.deps.Database().SaveBookmarks(ctx, false, book); saveErr != nil {
				d.deps.Logger().WithError(saveErr).WithField("url", book.URL).Error("failed to save bookmark download failure")
			}
			d.populateAdditionalFields(&book)
			return &book, nil
		}
		defer body.Close()
		content = body
		contentType = downloadType
	}

	processRequest := core.ProcessRequest{
		DataDir:     d.deps.Config().Storage.DataDir,
		Bookmark:    book,
		Content:     content,
		ContentType: contentType,
		// Keep user supplied metadata; only fall back to page metadata when the
		// user did not provide any.
		KeepTitle:   userHasDefinedTitle,
		KeepExcerpt: userHasDefinedExcerpt,
	}

	processed, isFatalErr, processErr := core.ProcessBookmark(d.deps, processRequest)
	if processErr != nil {
		d.deps.Logger().WithError(processErr).
			WithField("url", book.URL).
			WithField("step", processed.ProcessingFailedStep).
			Warn("bookmark processing failed")
		book = processed
		if isFatalErr {
			// Fatal errors (e.g. ebook generation) cannot produce the requested
			// artifact, but the bookmark row and its failure state are still
			// persisted so the UI can offer a retry.
			if _, saveErr := d.deps.Database().SaveBookmarks(ctx, false, book); saveErr != nil {
				d.deps.Logger().WithError(saveErr).WithField("url", book.URL).Error("failed to save bookmark processing failure")
			}
			d.populateAdditionalFields(&book)
			return &book, nil
		}
		// Non-fatal errors still produced usable metadata/content: persist it
		// together with the failure state so the UI keeps showing which step
		// failed after a refresh and a retry can clear it.
	} else {
		book = processed
	}

	if _, err := d.deps.Database().SaveBookmarks(ctx, false, book); err != nil {
		d.deps.Logger().WithError(err).WithField("url", book.URL).Error("failed to save processed bookmark")
		book.ProcessingStatus = model.BookmarkProcessingFailed
		book.ProcessingFailedStep = model.BookmarkStepSave
		book.ProcessingError = err.Error()
	}

	d.populateAdditionalFields(&book)
	return &book, nil
}

// BulkUpdateBookmarkTags updates tags for multiple bookmarks using tag IDs
func (d *BookmarksDomain) BulkUpdateBookmarkTags(ctx context.Context, bookmarkIDs []int, tagIDs []int) error {
	if len(bookmarkIDs) == 0 {
		return nil
	}

	// Call the database method directly
	err := d.deps.Database().BulkUpdateBookmarkTags(ctx, bookmarkIDs, tagIDs)
	if err != nil {
		return fmt.Errorf("failed to update bookmark tags: %w", err)
	}

	return nil
}

// AddTagToBookmark adds a tag to a bookmark
func (d *BookmarksDomain) AddTagToBookmark(ctx context.Context, bookmarkID int, tagID int) error {
	// Check if bookmark exists
	exists, err := d.BookmarkExists(ctx, bookmarkID)
	if err != nil {
		return err
	}
	if !exists {
		return model.ErrBookmarkNotFound
	}

	// Check if tag exists
	exists, err = d.deps.Domains().Tags().TagExists(ctx, tagID)
	if err != nil {
		return err
	}
	if !exists {
		return model.ErrTagNotFound
	}

	// Add tag to bookmark
	return d.deps.Database().AddTagToBookmark(ctx, bookmarkID, tagID)
}

// RemoveTagFromBookmark removes a tag from a bookmark
func (d *BookmarksDomain) RemoveTagFromBookmark(ctx context.Context, bookmarkID int, tagID int) error {
	// Check if bookmark exists
	exists, err := d.BookmarkExists(ctx, bookmarkID)
	if err != nil {
		return err
	}
	if !exists {
		return model.ErrBookmarkNotFound
	}

	// Check if tag exists
	exists, err = d.deps.Domains().Tags().TagExists(ctx, tagID)
	if err != nil {
		return err
	}
	if !exists {
		return model.ErrTagNotFound
	}

	// Remove tag from bookmark
	return d.deps.Database().RemoveTagFromBookmark(ctx, bookmarkID, tagID)
}

// BookmarkExists checks if a bookmark with the given ID exists
func (d *BookmarksDomain) BookmarkExists(ctx context.Context, id int) (bool, error) {
	return d.deps.Database().BookmarkExists(ctx, id)
}

// CreateBookmark creates a new bookmark
func (d *BookmarksDomain) CreateBookmark(ctx context.Context, bookmark model.Bookmark) (*model.BookmarkDTO, error) {
	// Convert to DTO for database operations
	dto := bookmark.ToDTO()

	// Save bookmark to database
	savedBookmarks, err := d.deps.Database().SaveBookmarks(ctx, true, dto)
	if err != nil {
		// Map database errors to domain errors
		if errors.Is(err, database.ErrBookmarkURLAlreadyExists) {
			return nil, model.ErrBookmarkAlreadyExists
		}
		return nil, fmt.Errorf("failed to save bookmark: %w", err)
	}

	if len(savedBookmarks) == 0 {
		return nil, fmt.Errorf("no bookmark was saved")
	}

	savedBookmark := savedBookmarks[0]

	// Populate additional properties
	d.populateAdditionalFields(&savedBookmark)

	return &savedBookmark, nil
}

// UpdateBookmark updates an existing bookmark
func (d *BookmarksDomain) UpdateBookmark(ctx context.Context, bookmark model.Bookmark) (*model.BookmarkDTO, error) {
	// Check if bookmark exists first
	exists, err := d.BookmarkExists(ctx, bookmark.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to check bookmark existence: %w", err)
	}
	if !exists {
		return nil, model.ErrBookmarkNotFound
	}

	// Convert to DTO for database operations
	dto := bookmark.ToDTO()

	// Update bookmark in database
	savedBookmarks, err := d.deps.Database().SaveBookmarks(ctx, false, dto)
	if err != nil {
		if errors.Is(err, database.ErrBookmarkURLAlreadyExists) {
			return nil, model.ErrBookmarkAlreadyExists
		}
		return nil, fmt.Errorf("failed to update bookmark: %w", err)
	}

	if len(savedBookmarks) == 0 {
		return nil, fmt.Errorf("no bookmark was updated")
	}

	savedBookmark := savedBookmarks[0]

	// Populate additional properties
	d.populateAdditionalFields(&savedBookmark)

	return &savedBookmark, nil
}

// DeleteBookmarks deletes multiple bookmarks by their IDs
func (d *BookmarksDomain) DeleteBookmarks(ctx context.Context, ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	err := d.deps.Database().DeleteBookmarks(ctx, ids...)
	if err != nil {
		return fmt.Errorf("failed to delete bookmarks: %w", err)
	}

	// The database rows are already gone, so a file that cannot be removed is only logged.
	for _, id := range ids {
		bookmark := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: id}}
		for _, path := range []string{
			model.GetThumbnailPath(bookmark),
			model.GetArchivePath(bookmark),
			model.GetEbookPath(bookmark),
		} {
			if err := d.deps.Domains().Storage().FS().Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
				d.deps.Logger().WithError(err).WithField("path", path).Warn("failed to remove bookmark file")
			}
		}
	}

	return nil
}

func NewBookmarksDomain(deps model.Dependencies) *BookmarksDomain {
	return &BookmarksDomain{
		deps: deps,
	}
}
