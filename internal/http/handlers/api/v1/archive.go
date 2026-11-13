package api_v1

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/http/middleware"
	"github.com/go-shiori/shiori/internal/http/response"
	"github.com/go-shiori/shiori/internal/model"
)

type archiveManifestResponse struct {
	BookmarkID int                     `json:"bookmark_id"`
	URL        string                  `json:"url"`
	CreatedAt  string                  `json:"created_at"`
	Total      int                     `json:"total"`
	OK         int                     `json:"ok"`
	Missing    int                     `json:"missing"`
	Skipped    int                     `json:"skipped"`
	Resources  []model.ArchiveResource `json:"resources"`
}

func newArchiveManifestResponse(manifest *model.ArchiveManifest) archiveManifestResponse {
	ok, missing, skipped := manifest.CountByStatus()
	return archiveManifestResponse{
		BookmarkID: manifest.BookmarkID,
		URL:        manifest.URL,
		CreatedAt:  manifest.CreatedAt,
		Total:      len(manifest.Resources),
		OK:         ok,
		Missing:    missing,
		Skipped:    skipped,
		Resources:  manifest.Resources,
	}
}

// getBookmarkForArchiveManifest retrieves the bookmark from the request path
// ID, sending the appropriate error response when it fails.
func getBookmarkForArchiveManifest(deps model.Dependencies, c model.WebContext) *model.BookmarkDTO {
	bookmarkID, err := strconv.Atoi(c.Request().PathValue("id"))
	if err != nil {
		response.SendError(c, http.StatusBadRequest, "Invalid bookmark ID")
		return nil
	}

	bookmark, err := deps.Domains().Bookmarks().GetBookmark(c.Request().Context(), model.DBID(bookmarkID))
	if err != nil {
		if errors.Is(err, model.ErrBookmarkNotFound) {
			response.SendError(c, http.StatusNotFound, "Bookmark not found")
			return nil
		}
		deps.Logger().WithError(err).Error("failed to get bookmark")
		response.SendInternalServerError(c)
		return nil
	}

	if !deps.Domains().Bookmarks().HasArchive(bookmark) {
		response.SendError(c, http.StatusNotFound, "Bookmark has no archive")
		return nil
	}

	return bookmark
}

// HandleGetArchiveManifest returns the resource manifest of a bookmark archive
//
//	@Summary	Get the resource manifest of a bookmark archive.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id	path	int	true	"Bookmark ID"
//	@Produce	json
//	@Success	200	{object}	archiveManifestResponse
//	@Failure	400	{object}	nil	"Invalid bookmark ID"
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark, archive or manifest not found"
//	@Router		/api/v1/bookmarks/{id}/archive/manifest [get]
func HandleGetArchiveManifest(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmark := getBookmarkForArchiveManifest(deps, c)
	if bookmark == nil {
		return
	}

	manifest, err := deps.Domains().Archiver().GetArchiveManifest(bookmark)
	if err != nil {
		if errors.Is(err, core.ErrArchiveManifestNotFound) {
			response.SendError(c, http.StatusNotFound, "Archive manifest not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to load archive manifest")
		response.SendInternalServerError(c)
		return
	}

	response.SendJSON(c, http.StatusOK, newArchiveManifestResponse(manifest))
}

type retryArchiveResourcesPayload struct {
	URLs []string `json:"urls"`
}

// HandleRetryArchiveResources retries the missing resources of a bookmark archive
//
//	@Summary	Retry the missing resources of a bookmark archive.
//	@Tags		Bookmarks
//	@Security	ApiKeyAuth
//	@Param		id		path	int								true	"Bookmark ID"
//	@Param		payload	body	retryArchiveResourcesPayload	false	"URLs to retry, all missing resources if empty"
//	@Produce	json
//	@Success	200	{object}	archiveManifestResponse
//	@Failure	400	{object}	nil	"Invalid bookmark ID or payload"
//	@Failure	403	{object}	nil	"Token not provided/invalid"
//	@Failure	404	{object}	nil	"Bookmark, archive or manifest not found"
//	@Router		/api/v1/bookmarks/{id}/archive/manifest/retry [post]
func HandleRetryArchiveResources(deps model.Dependencies, c model.WebContext) {
	if err := middleware.RequireLoggedInUser(deps, c); err != nil {
		return
	}

	bookmark := getBookmarkForArchiveManifest(deps, c)
	if bookmark == nil {
		return
	}

	// The payload is optional: without URLs, all missing resources are retried
	var payload retryArchiveResourcesPayload
	if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
		response.SendError(c, http.StatusBadRequest, "Invalid request payload")
		return
	}

	manifest, err := deps.Domains().Archiver().RetryArchiveResources(bookmark, payload.URLs)
	if err != nil {
		if errors.Is(err, core.ErrArchiveManifestNotFound) {
			response.SendError(c, http.StatusNotFound, "Archive manifest not found")
			return
		}
		deps.Logger().WithError(err).Error("failed to retry archive resources")
		response.SendInternalServerError(c)
		return
	}

	response.SendJSON(c, http.StatusOK, newArchiveManifestResponse(manifest))
}
