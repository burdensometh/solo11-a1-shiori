package domains

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

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
		return nil, fmt.Errorf("failed to download bookmark: %w", err)
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
		return nil, fmt.Errorf("failed to process bookmark: %w", err)
	}

	d.populateAdditionalFields(&processedBookmark)

	return &processedBookmark, nil
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

// DeleteBookmarks deletes multiple bookmarks by their IDs.
//
// The deletion is recorded in the database together with a cleanup task for
// each bookmark, then the files are removed right away. If any file cannot be
// removed, the task stays in the database and RunCleanupTasks retries it (for
// example on the next start).
func (d *BookmarksDomain) DeleteBookmarks(ctx context.Context, ids []int) error {
	if len(ids) == 0 {
		return nil
	}

	if err := d.deps.Database().DeleteBookmarks(ctx, ids...); err != nil {
		return fmt.Errorf("failed to delete bookmarks: %w", err)
	}

	// The database rows are already gone, so failures here must not fail the
	// whole deletion: the tasks are left behind for a later retry.
	for _, id := range ids {
		if err := d.cleanupBookmark(ctx, id); err != nil {
			d.deps.Logger().WithError(err).
				WithField("bookmark_id", id).
				Warn("failed to remove bookmark files, will retry later")
		}
	}

	return nil
}

// RunCleanupTasks processes every pending cleanup task left in the database.
// It is called at startup and after a deletion, so files whose removal failed
// earlier get another chance. Tasks are removed only once all of their files
// are gone; the rest are kept and retried next time.
func (d *BookmarksDomain) RunCleanupTasks(ctx context.Context) error {
	tasks, err := d.deps.Database().GetCleanupTasks(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cleanup tasks: %w", err)
	}

	remaining := 0
	for _, task := range tasks {
		if err := d.cleanupBookmark(ctx, task.BookmarkID); err != nil {
			remaining++
			d.deps.Logger().WithError(err).
				WithField("bookmark_id", task.BookmarkID).
				Warn("failed to remove bookmark files, will retry later")
		}
	}

	if remaining > 0 {
		d.deps.Logger().Warnf("%d bookmark cleanup task(s) could not be completed", remaining)
	}

	return nil
}

// cleanupBookmark removes the files of a bookmark and, only if every one of
// them is gone, the cleanup task recorded for it.
func (d *BookmarksDomain) cleanupBookmark(ctx context.Context, id int) error {
	bookmark := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: id}}
	fs := d.deps.Domains().Storage().FS()

	for _, path := range []string{
		model.GetThumbnailPath(bookmark),
		model.GetArchivePath(bookmark),
		model.GetEbookPath(bookmark),
	} {
		// An already missing file is not an error: removing it is idempotent.
		err := fs.Remove(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	tasks, err := d.deps.Database().GetCleanupTasks(ctx)
	if err != nil {
		return fmt.Errorf("failed to get cleanup tasks: %w", err)
	}
	for _, task := range tasks {
		if task.BookmarkID != id {
			continue
		}

		if err := d.deps.Database().DeleteCleanupTask(ctx, task.ID); err != nil {
			return fmt.Errorf("failed to delete cleanup task: %w", err)
		}
	}

	return nil
}

func NewBookmarksDomain(deps model.Dependencies) *BookmarksDomain {
	return &BookmarksDomain{
		deps: deps,
	}
}
