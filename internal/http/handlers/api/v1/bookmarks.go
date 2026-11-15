package api_v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/http/middleware"
	"github.com/go-shiori/shiori/internal/http/response"
	"github.com/go-shiori/shiori/internal/model"
)

type updateCachePayload struct {
	IDs           []int `validate:"required"`
	KeepMetadata  bool
	CreateArchive bool
	CreateEbook   bool
	SkipExist     bool
}

func (p *updateCachePayload) IsValid() error {
	if len(p.IDs) == 0 {
		return fmt.Errorf("id should not be empty")
	}
	for _, id := range p.IDs {
		if id <= 0 {
			return fmt.Errorf("id should not be 0 or negative")
		}
	}
	return nil
}

type readableResponseMessage struct {
	Content string
	HTML    string
}

type updateBookmarkDataPayload struct {
	UpdateReadable bool
	CreateArchive  bool
	CreateEbook    bool
	KeepMetadata   bool
	SkipExisting   bool
}

type bookmarkDataResponse struct {
	Content      string
	HTML         string
	ImageURL     string
	ArchiveURL   string
	EbookURL     string
	HasContent   bool
	HasArchive   bool
	HasEbook     bool
	HasThumbnail bool
}

// HandleBookmarkReadable returns the readable version of a bookmark
//
//	@Summary	Get readable version of bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id	path	int	true	"Bookmark ID"
//	@Produce	json
//	@Success	200	{object}	readableResponseMessage
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Router		/api/v1/bookmarks/{id}/readable [get]
func HandleBookmarkReadable(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	bookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return
	}

	response.SendJSON(c, http.StatusOK, readableResponseMessage{
		Content: bookmark.Content,
		HTML:    bookmark.HTML,
	})
}

// HandleGetBookmarkData returns the data and resource URLs for a bookmark
//
//	@Summary	Get bookmark data and resource URLs.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id	path	int	true	"Bookmark ID"
//	@Produce	json
//	@Success	200	{object}	bookmarkDataResponse
//	@Failure	400	{object}	nil	"Invalid bookmark ID"
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Router		/api/v1/bookmarks/{id}/data [get]
func HandleGetBookmarkData(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	bookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return
	}

	// Build resource URLs
	archiveURL := ""
	if bookmark.HasArchive {
		archiveURL = model.GetArchiveURL(bookmark)
	}

	ebookURL := ""
	if bookmark.HasEbook {
		ebookURL = model.GetEbookURL(bookmark)
	}

	imageURL := ""
	if bookmark.HasThumbnail {
		imageURL = model.GetThumbnailURL(bookmark)
	}

	response.SendJSON(c, http.StatusOK, bookmarkDataResponse{
		Content:      bookmark.Content,
		HTML:         bookmark.HTML,
		ImageURL:     imageURL,
		ArchiveURL:   archiveURL,
		EbookURL:     ebookURL,
		HasContent:   bookmark.HasContent,
		HasArchive:   bookmark.HasArchive,
		HasEbook:     bookmark.HasEbook,
		HasThumbnail: bookmark.HasThumbnail,
	})
}

// HandleUpdateBookmarkData updates the data for a bookmark (readable content, archive, ebook)
//
//	@Summary	Update bookmark data (content, archive, ebook).
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id		path	int							true	"Bookmark ID"
//	@Param		payload	body	updateBookmarkDataPayload	true	"Update Bookmark Data Payload"
//	@Produce	json
//	@Success	200	{object}	bookmarkDataResponse
//	@Failure	400	{object}	nil	"Invalid bookmark ID or payload"
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Failure	500	{object}	nil	"Failed to update bookmark data"
//	@Router		/api/v1/bookmarks/{id}/data [put]
func HandleUpdateBookmarkData(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	// Parse request payload
	var payload updateBookmarkDataPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	// Get bookmark from database
	bookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return
	}

	// Only update if at least one of the flags is true
	if payload.UpdateReadable || payload.CreateArchive || payload.CreateEbook {
		// Set flags on bookmark
		bookmark.CreateArchive = payload.CreateArchive
		bookmark.CreateEbook = payload.CreateEbook

		// Update bookmark cache
		updatedBookmark, err := deps.Domains().Bookmarks().UpdateBookmarkCache(
			c.Request().Context(),
			*bookmark,
			payload.KeepMetadata,
			payload.SkipExisting,
		)
		if err != nil {
			// A processing failure is not a request error: the bookmark still
			// exists, so answer 200 with the failure details attached. This lets
			// the UI show which step failed and offer another retry instead of
			// getting an opaque 500.
			deps.Logger().WithError(err).
				WithField("bookmark_id", bookmarkID).
				Warn("bookmark data update failed")
			if updatedBookmark != nil {
				bookmark = updatedBookmark
			} else {
				bookmark.ProcessingStatus = model.BookmarkProcessingFailed
				if bookmark.ProcessingFailedStep == "" {
					bookmark.ProcessingFailedStep = model.BookmarkStepDownload
				}
				bookmark.ProcessingError = err.Error()
			}

			// Persist the failure so it stays visible after a page reload.
			// SaveBookmarks is used directly because UpdateBookmark converts via
			// ToBookmark(), which would drop the processing-only DTO fields.
			if _, saveErr := deps.Database().SaveBookmarks(c.Request().Context(), false, *bookmark); saveErr != nil {
				deps.Logger().WithError(saveErr).Error("failed to persist bookmark processing failure")
			}
			response.SendJSON(c, http.StatusOK, bookmark)
			return
		}

		// Save updated bookmark to database
		savedBookmark, err := deps.Domains().Bookmarks().UpdateBookmark(c.Request().Context(), updatedBookmark.Bookmark)
		if err != nil {
			deps.Logger().WithError(err).Error("failed to save updated bookmark")
			response.SendError(c, http.StatusInternalServerError, "Failed to save bookmark")
			return
		}

		// Preserve the processing outcome reported by the pipeline.
		savedBookmark.ProcessingStatus = updatedBookmark.ProcessingStatus
		savedBookmark.ProcessingFailedStep = updatedBookmark.ProcessingFailedStep
		savedBookmark.ProcessingError = updatedBookmark.ProcessingError
		bookmark = savedBookmark
	}

	// Build resource URLs
	archiveURL := ""
	if bookmark.HasArchive {
		archiveURL = model.GetArchiveURL(bookmark)
	}

	ebookURL := ""
	if bookmark.HasEbook {
		ebookURL = model.GetEbookURL(bookmark)
	}

	imageURL := ""
	if bookmark.HasThumbnail {
		imageURL = model.GetThumbnailURL(bookmark)
	}

	response.SendJSON(c, http.StatusOK, bookmarkDataResponse{
		Content:      bookmark.Content,
		HTML:         bookmark.HTML,
		ImageURL:     imageURL,
		ArchiveURL:   archiveURL,
		EbookURL:     ebookURL,
		HasContent:   bookmark.HasContent,
		HasArchive:   bookmark.HasArchive,
		HasEbook:     bookmark.HasEbook,
		HasThumbnail: bookmark.HasThumbnail,
	})
}

// HandleUpdateCache updates the cache and ebook for bookmarks
//
//	@Summary	Update Cache and Ebook on server.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		payload	body	updateCachePayload	true	"Update Cache Payload"
//	@Produce	json
//	@Success	200	{object}	model.BookmarkDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Router		/api/v1/bookmarks/cache [put]
func HandleUpdateCache(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	// Parse request payload
	var payload updateCachePayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Get bookmarks from database
	bookmarks, err := deps.Domains().Bookmarks().GetBookmarks(c.Request().Context(), payload.IDs)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to get bookmarks")
		response.SendError(c, http.StatusInternalServerError, "Failed to get bookmarks")
		return
	}

	if len(bookmarks) == 0 {
		response.SendError(c, http.StatusNotFound, "No bookmarks found")
		return
	}

	// Process bookmarks concurrently
	mx := sync.RWMutex{}
	wg := sync.WaitGroup{}
	semaphore := make(chan struct{}, 10)

	for i, book := range bookmarks {
		wg.Add(1)

		book.CreateArchive = payload.CreateArchive
		book.CreateEbook = payload.CreateEbook

		go func(i int, book model.BookmarkDTO) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Download and process bookmark
			updatedBook, err := deps.Domains().Bookmarks().UpdateBookmarkCache(c.Request().Context(), book, payload.KeepMetadata, payload.SkipExist)
			if err != nil {
				// Record the failure on the bookmark instead of only logging it,
				// then persist it so the UI can show what went wrong.
				deps.Logger().WithError(err).
					WithField("bookmark_id", book.ID).
					Warn("error updating bookmark cache")
				if updatedBook != nil {
					book = *updatedBook
				} else {
					book.ProcessingStatus = model.BookmarkProcessingFailed
					if book.ProcessingFailedStep == "" {
						book.ProcessingFailedStep = model.BookmarkStepDownload
					}
					book.ProcessingError = err.Error()
				}
				if _, saveErr := deps.Database().SaveBookmarks(c.Request().Context(), false, book); saveErr != nil {
					deps.Logger().WithError(saveErr).
						WithField("bookmark_id", book.ID).
						Error("failed to persist bookmark processing failure")
				}
				updatedBook = &book
			} else if _, saveErr := deps.Database().SaveBookmarks(c.Request().Context(), false, *updatedBook); saveErr != nil {
				// Persist the processed content/archive and any step-level failure
				// reported by the pipeline. Without this the cache update only
				// existed in the response and was lost on reload.
				deps.Logger().WithError(saveErr).
					WithField("bookmark_id", updatedBook.ID).
					Error("failed to save updated bookmark cache")
				updatedBook.ProcessingStatus = model.BookmarkProcessingFailed
				updatedBook.ProcessingFailedStep = model.BookmarkStepSave
				updatedBook.ProcessingError = saveErr.Error()
			}

			mx.Lock()
			bookmarks[i] = *updatedBook
			mx.Unlock()
		}(i, book)
	}

	wg.Wait()

	response.SendJSON(c, http.StatusOK, bookmarks)
}

type bulkUpdateBookmarkTagsPayload struct {
	BookmarkIDs []int `validate:"required"`
	TagIDs      []int `validate:"required"`
}

func (p *bulkUpdateBookmarkTagsPayload) IsValid() error {
	if len(p.BookmarkIDs) == 0 {
		return fmt.Errorf("bookmark_ids should not be empty")
	}
	if len(p.TagIDs) == 0 {
		return fmt.Errorf("tag_ids should not be empty")
	}
	return nil
}

// HandleGetBookmarkTags gets the tags for a bookmark
//
//	@Summary	Get tags for a bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Produce	json
//	@Param		id	path		int	true	"Bookmark ID"
//	@Success	200	{array}		model.TagDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Router		/api/v1/bookmarks/{id}/tags [get]
func HandleGetBookmarkTags(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	// Check if bookmark exists
	exists, err := deps.Domains().Bookmarks().BookmarkExists(c.Request().Context(), bookmarkID)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to check bookmark existence")
		response.SendError(c, http.StatusInternalServerError, "Failed to check if bookmark exists")
		return
	}
	if !exists {
		response.SendError(c, http.StatusNotFound, "Bookmark not found")
		return
	}

	// Get bookmark to retrieve its tags
	tags, err := deps.Domains().Tags().ListTags(c.Request().Context(), model.ListTagsOptions{
		BookmarkID: bookmarkID,
	})
	if err != nil {
		deps.Logger().WithError(err).Error("failed to get bookmark tags")
		response.SendError(c, http.StatusInternalServerError, "Failed to get bookmark tags")
		return
	}

	response.SendJSON(c, http.StatusOK, tags)
}

// bookmarkTagPayload is used for both adding and removing tags from bookmarks
type bookmarkTagPayload struct {
	TagID int `validate:"required"`
}

func (p *bookmarkTagPayload) IsValid() error {
	if p.TagID <= 0 {
		return fmt.Errorf("tag_id should be a positive integer")
	}
	return nil
}

// HandleAddTagToBookmark adds a tag to a bookmark
//
//	@Summary	Add a tag to a bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id		path	int					true	"Bookmark ID"
//	@Param		payload	body	bookmarkTagPayload	true	"Add Tag Payload"
//	@Produce	json
//	@Success	200	{object}	nil
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark or tag not found"
//	@Router		/api/v1/bookmarks/{id}/tags [post]
func HandleAddTagToBookmark(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	// Parse request payload
	var payload bookmarkTagPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Add tag to bookmark
	err = deps.Domains().Bookmarks().AddTagToBookmark(c.Request().Context(), bookmarkID, payload.TagID)
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		if errors.Is(err, model.ErrTagNotFound) {
			response.SendError(c, http.StatusNotFound, "Tag not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to add tag to bookmark")
		response.SendError(c, http.StatusInternalServerError, "Failed to add tag to bookmark")
		return
	}

	response.SendJSON(c, http.StatusCreated, nil)
}

// HandleRemoveTagFromBookmark removes a tag from a bookmark
//
//	@Summary	Remove a tag from a bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id		path	int					true	"Bookmark ID"
//	@Param		payload	body	bookmarkTagPayload	true	"Remove Tag Payload"
//	@Produce	json
//	@Success	200	{object}	nil
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Router		/api/v1/bookmarks/{id}/tags [delete]
func HandleRemoveTagFromBookmark(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	// Parse request payload
	var payload bookmarkTagPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Remove tag from bookmark
	err = deps.Domains().Bookmarks().RemoveTagFromBookmark(c.Request().Context(), bookmarkID, payload.TagID)
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		if errors.Is(err, model.ErrTagNotFound) {
			response.SendError(c, http.StatusNotFound, "Tag not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to remove tag from bookmark")
		response.SendError(c, http.StatusInternalServerError, "Failed to remove tag from bookmark")
		return
	}

	response.SendJSON(c, http.StatusOK, nil)
}

// Bookmark CRUD operations

type createBookmarkOptions struct {
	CreateArchive bool
	CreateEbook   bool
}

type createBookmarkPayload struct {
	URL     string                 `validate:"required"`
	Title   string                 `json:",omitempty"`
	Excerpt string                 `json:",omitempty"`
	Public  *int                   `json:",omitempty"`
	Options *createBookmarkOptions `json:",omitempty"`
}

// IsQuickAdd returns true if only the URL was provided (quick add scenario)
func (p *createBookmarkPayload) IsQuickAdd() bool {
	hasTitle := strings.TrimSpace(p.Title) != ""
	hasExcerpt := strings.TrimSpace(p.Excerpt) != ""
	hasPublic := p.Public != nil
	hasOptions := p.Options != nil && (p.Options.CreateArchive || p.Options.CreateEbook)

	// Quick add if only URL is provided (no title, excerpt, public flag, or options)
	return !hasTitle && !hasExcerpt && !hasPublic && !hasOptions
}

func (p *createBookmarkPayload) IsValid() error {
	if strings.TrimSpace(p.URL) == "" {
		return fmt.Errorf("url should not be empty")
	}
	return nil
}

// HandleCreateBookmark creates a new bookmark
//
//	@Summary	Create a new bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		payload	body	createBookmarkPayload	true	"Create Bookmark Payload"
//	@Produce	json
//	@Success	201	{object}	model.BookmarkDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	400	{object}	nil	"Invalid request payload"
//	@Router		/api/v1/bookmarks [post]
func HandleCreateBookmark(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	// Parse request payload
	var payload createBookmarkPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	cleanURL, err := core.RemoveUTMParams(strings.TrimSpace(payload.URL))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid URL")
		return
	}
	payload.URL = cleanURL

	// Set Public field if provided, otherwise default to 0 (internal)
	public := 0
	if payload.Public != nil {
		public = *payload.Public
	}

	// Archive/ebook options. By default every entry point creates an archive
	// unless the caller explicitly opts out, which keeps the web UI and the
	// browser extension consistent.
	createArchive := true
	createEbook := false
	if payload.Options != nil {
		createArchive = payload.Options.CreateArchive
		createEbook = payload.Options.CreateEbook
	}

	// Run the shared ingestion pipeline: it persists the bookmark, downloads and
	// processes the page and reports per-step failures on the returned DTO
	// instead of hiding them in server logs.
	ingested, err := deps.Domains().Bookmarks().IngestBookmark(c.Request().Context(), model.IngestBookmarkRequest{
		URL:           payload.URL,
		Title:         payload.Title,
		Excerpt:       payload.Excerpt,
		Public:        public,
		CreateArchive: createArchive,
		CreateEbook:   createEbook,
	})
	if err != nil {
		deps.Logger().WithError(err).Error("error creating bookmark")
		if errors.Is(err, model.ErrBookmarkAlreadyExists) {
			response.SendError(c, http.StatusConflict, "already_exists.bookmark")
			return
		}
		response.SendError(c, http.StatusInternalServerError, "Failed to create bookmark")
		return
	}

	// The ingestion pipeline returns 201 even when the background processing
	// failed: the bookmark itself was created and the DTO carries the failure
	// details so the client can display it and trigger a retry.
	response.SendJSON(c, http.StatusCreated, ingested)
}

// HandleListBookmarks lists bookmarks with optional filtering
//
//	@Summary	List bookmarks with optional filtering and pagination.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		keyword	query	string	false	"Search keyword"
//	@Param		tags	query	string	false	"Comma-separated list of tags to include"
//	@Param		exclude	query	string	false	"Comma-separated list of tags to exclude"
//	@Param		page	query	int		false	"Page number (default: 1)"
//	@Param		limit	query	int		false	"Items per page (default: 30, max: 100)"
//	@Produce	json
//	@Success	200	{object}	model.PaginatedResponse[model.BookmarkDTO]
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Router		/api/v1/bookmarks [get]
func HandleListBookmarks(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	// Parse query parameters
	query := c.Request().URL.Query()
	keyword := query.Get("keyword")
	tagsStr := query.Get("tags")
	excludedTagsStr := query.Get("exclude")
	pageStr := query.Get("page")
	limitStr := query.Get("limit")

	// Parse tags
	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
	}

	var excludedTags []string
	if excludedTagsStr != "" {
		excludedTags = strings.Split(excludedTagsStr, ",")
	}

	// Parse page and limit
	page := 1
	if pageStr != "" {
		p, err := strconv.Atoi(pageStr)
		if err != nil || p <= 0 {
			response.SendError(c, http.StatusBadRequest, "Invalid page parameter: must be a positive integer")
			return
		}
		page = p
	}

	limit := 30
	if limitStr != "" {
		l, err := strconv.Atoi(limitStr)
		if err != nil || l <= 0 || l > 100 {
			response.SendError(c, http.StatusBadRequest, "Invalid limit parameter: must be an integer between 1 and 100")
			return
		}
		limit = l
	}

	// Prepare search options using domain-level options
	searchOptions := model.BookmarksSearchOptions{
		Tags:         tags,
		ExcludedTags: excludedTags,
		Keyword:      keyword,
		Limit:        limit,
		Offset:       (page - 1) * limit,
		OrderMethod:  model.ByLastAddedSearchOrder,
	}

	// Search bookmarks through domain layer
	bookmarks, err := deps.Domains().Bookmarks().SearchBookmarks(c.Request().Context(), searchOptions)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to search bookmarks")
		response.SendError(c, http.StatusInternalServerError, "Failed to search bookmarks")
		return
	}

	// Get total count
	countOptions := model.BookmarksSearchOptions{
		Tags:         tags,
		ExcludedTags: excludedTags,
		Keyword:      keyword,
	}
	totalCount, err := deps.Domains().Bookmarks().CountBookmarks(c.Request().Context(), countOptions)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to get bookmarks count")
		response.SendError(c, http.StatusInternalServerError, "Failed to get bookmarks count")
		return
	}

	// Return paginated response
	paginatedResponse := model.PaginatedResponse[model.BookmarkDTO]{
		Items: bookmarks,
		Total: totalCount,
	}

	response.SendJSON(c, http.StatusOK, paginatedResponse)
}

// HandleGetBookmark gets a single bookmark by ID
//
//	@Summary	Get a bookmark by ID.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id	path	int	true	"Bookmark ID"
//	@Produce	json
//	@Success	200	{object}	model.BookmarkDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Router		/api/v1/bookmarks/{id} [get]
func HandleGetBookmark(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	bookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return
	}

	response.SendJSON(c, http.StatusOK, bookmark)
}

type updateBookmarkPayload struct {
	URL     *string
	Title   *string
	Excerpt *string
	Public  *int
}

// HandleUpdateBookmark updates an existing bookmark
//
//	@Summary	Update an existing bookmark.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id		path	int						true	"Bookmark ID"
//	@Param		payload	body	updateBookmarkPayload	true	"Update Bookmark Payload"
//	@Produce	json
//	@Success	200	{object}	model.BookmarkDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark not found"
//	@Failure	400	{object}	nil	"Invalid request payload"
//	@Router		/api/v1/bookmarks/{id} [put]
func HandleUpdateBookmark(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return
	}

	// Parse request payload
	var payload updateBookmarkPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	// Get existing bookmark
	existingBookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return
	}

	// Convert to Bookmark for domain operations
	bookmark := existingBookmark.ToBookmark()

	// Validate URL if provided
	if payload.URL != nil && strings.TrimSpace(*payload.URL) == "" {
		response.SendError(c, http.StatusBadRequest, "URL should not be empty")
		return
	}

	// Update fields if provided
	if payload.URL != nil {
		cleanURL, err := core.RemoveUTMParams(strings.TrimSpace(*payload.URL))
		if err != nil {
			response.SendError(c, http.StatusBadRequest, "Invalid URL")
			return
		}
		bookmark.URL = cleanURL
	}
	if payload.Title != nil {
		bookmark.Title = *payload.Title
	}
	if payload.Excerpt != nil {
		bookmark.Excerpt = *payload.Excerpt
	}
	if payload.Public != nil {
		bookmark.Public = *payload.Public
	}

	// Update the bookmark
	updatedBookmark, err := deps.Domains().Bookmarks().UpdateBookmark(c.Request().Context(), bookmark)
	if errors.Is(err, model.ErrBookmarkAlreadyExists) {
		response.SendError(c, http.StatusConflict, "already_exists.bookmark")
		return
	}
	if err != nil {
		deps.Logger().WithError(err).Error("failed to update bookmark")
		response.SendError(c, http.StatusInternalServerError, "Failed to update bookmark")
		return
	}

	response.SendJSON(c, http.StatusOK, updatedBookmark)
}

type deleteBookmarksPayload struct {
	IDs []int `validate:"required"`
}

func (p *deleteBookmarksPayload) IsValid() error {
	if len(p.IDs) == 0 {
		return fmt.Errorf("ids should not be empty")
	}
	for _, id := range p.IDs {
		if id <= 0 {
			return fmt.Errorf("id should not be 0 or negative")
		}
	}
	return nil
}

// HandleDeleteBookmarks deletes one or more bookmarks
//
//	@Summary	Delete one or more bookmarks.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		payload	body	deleteBookmarksPayload	true	"Delete Bookmarks Payload"
//	@Produce	json
//	@Success	200	{object}	nil
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	400	{object}	nil	"Invalid request payload"
//	@Router		/api/v1/bookmarks [delete]
func HandleDeleteBookmarks(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	// Parse request payload
	var payload deleteBookmarksPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Delete bookmarks
	err := deps.Domains().Bookmarks().DeleteBookmarks(c.Request().Context(), payload.IDs)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to delete bookmarks")
		response.SendError(c, http.StatusInternalServerError, "Failed to delete bookmarks")
		return
	}

	response.SendJSON(c, http.StatusOK, nil)
}

// HandleBulkUpdateBookmarkTags updates the tags for multiple bookmarks
//
//	@Summary	Bulk update tags for multiple bookmarks.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		payload	body	bulkUpdateBookmarkTagsPayload	true	"Bulk Update Bookmark Tags Payload"
//	@Produce	json
//	@Success	200	{object}	[]model.BookmarkDTO
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	400	{object}	nil	"Invalid request payload"
//	@Failure	404	{object}	nil	"No bookmarks found"
//	@Router		/api/v1/bookmarks/bulk/tags [put]
func HandleBulkUpdateBookmarkTags(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInAdmin(deps, c); err != nil {
		return
	}

	// Parse request payload
	var payload bulkUpdateBookmarkTagsPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	if err := payload.IsValid(); err != nil {
		response.SendError(c, http.StatusBadRequest, err.Error())
		return
	}

	// Use the domain method to update bookmark tags
	err := deps.Domains().Bookmarks().BulkUpdateBookmarkTags(c.Request().Context(), payload.BookmarkIDs, payload.TagIDs)
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "No bookmarks found")
			return
		}
		deps.Logger().WithError(err).Error("failed to update bookmark tags")
		response.SendError(c, http.StatusInternalServerError, "Failed to update bookmarks")
		return
	}

	bookmarks, err := deps.Domains().Bookmarks().GetBookmarks(c.Request().Context(), payload.BookmarkIDs)
	if err != nil {
		deps.Logger().WithError(err).Error("failed to get updated bookmarks")
		response.SendInternalServerError(c)
		return
	}

	response.SendJSON(c, http.StatusOK, bookmarks)
}
