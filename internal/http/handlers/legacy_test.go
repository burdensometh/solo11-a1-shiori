package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SetFakeAuthorizationHeader sets a fake authorization header for the request in order to have
// a valid session. If we don't set this the `validateSession` function will return an error.
func SetFakeAuthorizationHeader(t *testing.T, deps model.Dependencies, c model.WebContext) {
	token, err := deps.Domains().Auth().CreateTokenForAccount(c.GetAccount(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	c.Request().Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
}

func TestLegacyHandler(t *testing.T) {
	logger := logrus.New()
	_, deps := testutil.GetTestConfigurationAndDependencies(t, context.Background(), logger)
	handler := NewLegacyHandler(deps)

	t.Run("HandleLogin", func(t *testing.T) {
		account := &model.AccountDTO{
			ID:       1,
			Username: "test",
			Owner:    model.Ptr(false),
		}

		sessionID, err := handler.HandleLogin(account, time.Hour)
		require.NoError(t, err)
		require.NotEmpty(t, sessionID)
	})

	t.Run("HandleGetTags", func(t *testing.T) {
		c, w := testutil.NewTestWebContext()

		// Create a real account in the database
		account, err := deps.Domains().Accounts().CreateAccount(context.Background(), model.AccountDTO{
			Username: "testuser",
			Password: "testpass",
			Owner:    model.Ptr(false),
		})
		require.NoError(t, err)

		// Set the real account in the context
		c.SetAccount(account)
		SetFakeAuthorizationHeader(t, deps, c)
		handler.HandleGetTags(deps, c)
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("HandleGetBookmarks", func(t *testing.T) {
		c, w := testutil.NewTestWebContext()

		// Create a real account in the database
		account, err := deps.Domains().Accounts().CreateAccount(context.Background(), model.AccountDTO{
			Username: "testuser2",
			Password: "testpass2",
			Owner:    model.Ptr(false),
		})
		require.NoError(t, err)

		// Set the real account in the context
		c.SetAccount(account)
		SetFakeAuthorizationHeader(t, deps, c)
		handler.HandleGetBookmarks(deps, c)
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("convertParams", func(t *testing.T) {
		r, _ := http.NewRequest(http.MethodGet, "/api/bookmarks?page=1&tags=test,dev", http.NoBody)
		params := handler.convertParams(r)

		require.Len(t, params, 2)

		// Create a map to check for parameters regardless of order
		paramMap := make(map[string]string)
		for _, param := range params {
			paramMap[param.Key] = param.Value
		}

		// Check that both parameters exist with the correct values
		require.Contains(t, paramMap, "page")
		require.Equal(t, "1", paramMap["page"])
		require.Contains(t, paramMap, "tags")
		require.Equal(t, "test,dev", paramMap["tags"])
	})
}

const extTestHTML = `<!DOCTYPE html>
<html>
	<head><title>Page Metadata Title</title></head>
	<body><article><p>Readable body content with enough words to be parsed as an article.</p></article></body>
</html>`

func newExtensionContext(t *testing.T, deps model.Dependencies, payload string) (model.WebContext, *httptest.ResponseRecorder) {
	t.Helper()

	account, err := deps.Domains().Accounts().CreateAccount(context.Background(), model.AccountDTO{
		Username: fmt.Sprintf("ext-owner-%d", time.Now().UnixNano()),
		Password: "ext-pass",
		Owner:    model.Ptr(true),
	})
	require.NoError(t, err)

	c, w := testutil.NewTestWebContextWithMethod(http.MethodPost, "/api/bookmarks/ext",
		testutil.WithAccount(account),
		testutil.WithBody(payload),
	)
	SetFakeAuthorizationHeader(t, deps, c)
	return c, w
}

func TestHandleInsertViaExtension(t *testing.T) {
	logger := logrus.New()
	_, deps := testutil.GetTestConfigurationAndDependencies(t, context.Background(), logger)
	handler := NewLegacyHandler(deps)

	t.Run("defaults to creating an archive and keeps user title", func(t *testing.T) {
		payload := fmt.Sprintf(`{
			"url": "http://127.0.0.1:1/extension-default-archive",
			"title": "User supplied title",
			"html": %q
		}`, extTestHTML)

		c, w := newExtensionContext(t, deps, payload)
		handler.HandleInsertViaExtension(deps, c)
		require.Equal(t, http.StatusOK, w.Code)

		var book model.BookmarkDTO
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &book))
		assert.NotZero(t, book.ID)
		assert.Equal(t, "User supplied title", book.Title)
		// No create_archive flag was sent, but the default must match the web
		// entry point: an archive is created.
		assert.True(t, book.HasArchive)
		assert.Equal(t, model.BookmarkProcessingSuccess, book.ProcessingStatus)
	})

	t.Run("reports failed steps on the response", func(t *testing.T) {
		// No HTML provided and an unroutable URL: the download step must fail in
		// a way the client can see, instead of only ending up in server logs.
		payload := `{"url": "http://127.0.0.1:1/extension-failing-download", "title": "Failing"}`

		c, w := newExtensionContext(t, deps, payload)
		handler.HandleInsertViaExtension(deps, c)
		require.Equal(t, http.StatusOK, w.Code)

		var book model.BookmarkDTO
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &book))
		assert.Equal(t, model.BookmarkProcessingFailed, book.ProcessingStatus)
		assert.Equal(t, model.BookmarkStepDownload, book.ProcessingFailedStep)
		assert.NotEmpty(t, book.ProcessingError)
		assert.False(t, book.HasArchive)
	})

	t.Run("retry reuses the same bookmark and clears the failure", func(t *testing.T) {
		failingPayload := `{"url": "http://127.0.0.1:1/extension-retry", "title": "Retry me"}`

		c1, w1 := newExtensionContext(t, deps, failingPayload)
		handler.HandleInsertViaExtension(deps, c1)
		require.Equal(t, http.StatusOK, w1.Code)

		var first model.BookmarkDTO
		require.NoError(t, json.Unmarshal(w1.Body.Bytes(), &first))
		assert.Equal(t, model.BookmarkProcessingFailed, first.ProcessingStatus)

		// Retry with rendered HTML, like the extension does on a second attempt.
		retryPayload := fmt.Sprintf(`{
			"url": "http://127.0.0.1:1/extension-retry",
			"title": "Retry me",
			"html": %q
		}`, extTestHTML)

		c2, w2 := newExtensionContext(t, deps, retryPayload)
		handler.HandleInsertViaExtension(deps, c2)
		require.Equal(t, http.StatusOK, w2.Code)

		var second model.BookmarkDTO
		require.NoError(t, json.Unmarshal(w2.Body.Bytes(), &second))
		assert.Equal(t, first.ID, second.ID, "retry must update the existing row, not create one")
		assert.Equal(t, model.BookmarkProcessingSuccess, second.ProcessingStatus)
		assert.True(t, second.HasArchive)
	})

	t.Run("explicitly disabled archive is not created", func(t *testing.T) {
		payload := fmt.Sprintf(`{
			"url": "http://127.0.0.1:1/extension-no-archive",
			"title": "No archive please",
			"html": %q,
			"create_archive": false
		}`, extTestHTML)

		c, w := newExtensionContext(t, deps, payload)
		handler.HandleInsertViaExtension(deps, c)
		require.Equal(t, http.StatusOK, w.Code)

		var book model.BookmarkDTO
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &book))
		assert.False(t, book.HasArchive)
	})

	t.Run("invalid payload is rejected with a clear error", func(t *testing.T) {
		c, w := newExtensionContext(t, deps, "not json")
		handler.HandleInsertViaExtension(deps, c)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}
