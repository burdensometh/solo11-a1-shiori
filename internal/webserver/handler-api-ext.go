package webserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	fp "path/filepath"
	"strconv"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/julienschmidt/httprouter"
)

// extensionBookmarkPayload is the payload sent by the browser extension. It
// mirrors model.BookmarkDTO but the archive/ebook flags are pointers so an
// absent value falls back to the same default as the web entry point (archive
// enabled, ebook disabled) instead of silently disabling the archive.
type extensionBookmarkPayload struct {
	URL           string         `json:"url"`
	Title         string         `json:"title"`
	Excerpt       string         `json:"excerpt"`
	Tags          []model.TagDTO `json:"tags"`
	HTML          string         `json:"html"`
	Public        int            `json:"public"`
	CreateArchive *bool          `json:"create_archive"`
	CreateEbook   *bool          `json:"create_ebook"`
}

// ApiInsertViaExtension is handler for POST /api/bookmarks/ext
//
// It goes through the same ingestion pipeline as the v1 web API so the
// extension and the web UI produce identical results for a given URL: the same
// archive/ebook defaults, the same handling of user supplied titles and the
// same per-step processing status on the returned bookmark.
func (h *Handler) ApiInsertViaExtension(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()

	// Make sure session still valid
	if err := h.validateSession(r); err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Decode request
	var payload extensionBookmarkPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, fmt.Sprintf("invalid request payload: %s", err), http.StatusBadRequest)
		return
	}

	// Defaults must match HandleCreateBookmark in the v1 API: create an archive
	// unless explicitly disabled, never create an ebook unless explicitly enabled.
	createArchive := true
	if payload.CreateArchive != nil {
		createArchive = *payload.CreateArchive
	}
	createEbook := false
	if payload.CreateEbook != nil {
		createEbook = *payload.CreateEbook
	}

	book, err := h.dependencies.Domains().Bookmarks().IngestBookmark(ctx, model.IngestBookmarkRequest{
		URL:            payload.URL,
		Title:          payload.Title,
		Excerpt:        payload.Excerpt,
		Public:         payload.Public,
		Tags:           payload.Tags,
		Content:        []byte(payload.HTML),
		CreateArchive:  createArchive,
		CreateEbook:    createEbook,
		UpdateIfExists: true,
	})
	if err != nil {
		h.dependencies.Logger().WithError(err).WithField("url", payload.URL).Error("failed to ingest extension bookmark")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Return the bookmark, including ProcessingStatus/ProcessingFailedStep so the
	// extension can surface failed fetches instead of guessing from file flags.
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(book); err != nil {
		h.dependencies.Logger().WithError(err).Error("failed to encode extension response")
	}
}

// ApiDeleteViaExtension is handler for DELETE /api/bookmark/ext
func (h *Handler) ApiDeleteViaExtension(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	ctx := r.Context()

	// Make sure session still valid
	err := h.validateSession(r)
	checkError(err)

	// Decode request
	request := model.BookmarkDTO{}
	err = json.NewDecoder(r.Body).Decode(&request)
	checkError(err)

	// Check if bookmark already exists.
	book, exist, err := h.DB.GetBookmark(ctx, 0, request.URL)
	checkError(err)

	if exist {
		// Delete bookmarks
		err = h.DB.DeleteBookmarks(ctx, book.ID)
		checkError(err)

		// Delete thumbnail image and archives from local disk
		strID := strconv.Itoa(book.ID)
		imgPath := fp.Join(h.DataDir, "thumb", strID)
		archivePath := fp.Join(h.DataDir, "archive", strID)

		os.Remove(imgPath)
		os.Remove(archivePath)
	}

	fmt.Fprint(w, 1)
}
