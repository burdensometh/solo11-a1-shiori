package domains_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-shiori/shiori/internal/domains"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/sirupsen/logrus"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ingestTestHTML = `<!DOCTYPE html>
<html>
	<head>
		<title>Page Title From Metadata</title>
		<meta name="description" content="Page excerpt from metadata">
	</head>
	<body>
		<article><p>This is a readable article body with enough text to be considered content.</p></article>
	</body>
</html>`

func newIngestTestServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=UTF-8")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func TestBookmarksDomain_IngestBookmark(t *testing.T) {
	ctx := context.Background()
	logger := logrus.New()

	t.Run("creates bookmark and keeps user title", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		server := newIngestTestServer(t, http.StatusOK, ingestTestHTML)
		defer server.Close()

		result, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:           server.URL + "/keep-title",
			Title:         "My custom title",
			Excerpt:       "My custom excerpt",
			CreateArchive: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)
		require.NotZero(t, result.ID)

		// User supplied metadata must not be replaced by page metadata.
		assert.Equal(t, "My custom title", result.Title)
		assert.Equal(t, "My custom excerpt", result.Excerpt)

		// Processing succeeded and the archive was actually written.
		assert.Equal(t, model.BookmarkProcessingSuccess, result.ProcessingStatus)
		assert.Empty(t, result.ProcessingFailedStep)
		assert.True(t, result.HasArchive)
		assert.True(t, deps.Domains().Storage().FileExists(model.GetArchivePath(result)))
	})

	t.Run("falls back to page metadata when no title supplied", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		server := newIngestTestServer(t, http.StatusOK, ingestTestHTML)
		defer server.Close()

		result, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:           server.URL + "/no-title",
			CreateArchive: true,
		})
		require.NoError(t, err)
		assert.Equal(t, "Page Title From Metadata", result.Title)
		assert.Equal(t, model.BookmarkProcessingSuccess, result.ProcessingStatus)
	})

	t.Run("download failure is reported on the DTO without request error", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		// Close the server immediately so every connection is refused, which
		// surfaces as a transport error during the download step.
		server := newIngestTestServer(t, http.StatusInternalServerError, "boom")
		serverURL := server.URL
		server.Close()

		result, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:           serverURL + "/failing",
			Title:         "Failing page",
			CreateArchive: true,
		})
		require.NoError(t, err)
		require.NotNil(t, result)

		// The bookmark still exists, but the failure step/message are visible.
		assert.Equal(t, model.BookmarkProcessingFailed, result.ProcessingStatus)
		assert.Equal(t, model.BookmarkStepDownload, result.ProcessingFailedStep)
		assert.NotEmpty(t, result.ProcessingError)
		assert.False(t, result.HasArchive)

		// The row was persisted with the user title.
		stored, getErr := domain.GetBookmark(ctx, model.DBID(result.ID))
		require.NoError(t, getErr)
		assert.Equal(t, "Failing page", stored.Title)

		// The failure state must survive a fresh read so the UI can still show
		// it after a page reload.
		assert.Equal(t, model.BookmarkProcessingFailed, stored.ProcessingStatus)
		assert.Equal(t, model.BookmarkStepDownload, stored.ProcessingFailedStep)
		assert.NotEmpty(t, stored.ProcessingError)
	})

	t.Run("retry updates the processing status", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		server := newIngestTestServer(t, http.StatusInternalServerError, "boom")
		serverURL := server.URL
		server.Close()

		first, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:            serverURL + "/retry",
			CreateArchive:  true,
			UpdateIfExists: true,
		})
		require.NoError(t, err)
		assert.Equal(t, model.BookmarkProcessingFailed, first.ProcessingStatus)

		// Retry using provided HTML content, which is exactly what the extension
		// does when it re-sends a page it could render locally.
		retry, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:            first.URL,
			Content:        []byte(ingestTestHTML),
			ContentType:    "text/html; charset=UTF-8",
			CreateArchive:  true,
			UpdateIfExists: true,
		})
		require.NoError(t, err)
		assert.Equal(t, first.ID, retry.ID)
		assert.Equal(t, model.BookmarkProcessingSuccess, retry.ProcessingStatus)
		assert.True(t, retry.HasArchive)
		assert.Empty(t, retry.ProcessingError)
	})

	t.Run("duplicate URL without update flag is rejected", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		server := newIngestTestServer(t, http.StatusOK, ingestTestHTML)
		defer server.Close()

		request := model.IngestBookmarkRequest{
			URL:           server.URL + "/duplicate",
			Title:         "First",
			CreateArchive: true,
		}
		first, err := domain.IngestBookmark(ctx, request)
		require.NoError(t, err)

		_, err = domain.IngestBookmark(ctx, request)
		require.ErrorIs(t, err, model.ErrBookmarkAlreadyExists)

		// Extension-style re-add reuses the row instead of failing.
		request.UpdateIfExists = true
		request.Title = "Second"
		second, err := domain.IngestBookmark(ctx, request)
		require.NoError(t, err)
		assert.Equal(t, first.ID, second.ID)
	})

	t.Run("provided HTML skips download and creates archive", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		// Unroutable URL: the test proves the provided content is used without
		// attempting any network fetch.
		result, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:            "http://127.0.0.1:1/extension-provided",
			Title:          "Extension page",
			Content:        []byte(ingestTestHTML),
			CreateArchive:  true,
			UpdateIfExists: true,
		})
		require.NoError(t, err)
		assert.Equal(t, model.BookmarkProcessingSuccess, result.ProcessingStatus)
		assert.True(t, result.HasArchive)
		assert.Equal(t, "Extension page", result.Title)
		assert.True(t, deps.Domains().Storage().FileExists(model.GetArchivePath(result)))
	})

	t.Run("archive disabled means no archive file", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		fs := afero.NewMemMapFs()
		deps.Domains().SetStorage(domains.NewStorageDomain(deps, fs))
		domain := domains.NewBookmarksDomain(deps)

		server := newIngestTestServer(t, http.StatusOK, ingestTestHTML)
		defer server.Close()

		result, err := domain.IngestBookmark(ctx, model.IngestBookmarkRequest{
			URL:           server.URL + "/no-archive",
			CreateArchive: false,
		})
		require.NoError(t, err)
		assert.False(t, result.HasArchive)
		assert.False(t, deps.Domains().Storage().FileExists(model.GetArchivePath(result)))
	})
}
