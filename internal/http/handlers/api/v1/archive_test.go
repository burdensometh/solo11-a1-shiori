package api_v1

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/go-shiori/shiori/internal/config"
	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/dependencies"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// archiveManifestTestFixture holds a bookmark with a real archive and
// manifest created from a test server, plus the server switches.
type archiveManifestTestFixture struct {
	cfg    *config.Config
	deps   *dependencies.Dependencies
	book   model.BookmarkDTO
	broken map[string]bool
}

func newArchiveManifestTestFixture(t *testing.T, ctx context.Context) *archiveManifestTestFixture {
	t.Helper()

	logger := logrus.New()
	cfg, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
	cfg.Archive.ExcludedDomains = []string{"analytics.invalid"}

	broken := map[string]bool{"/img/broken.png": true}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head>
			<title>Test page</title>
			<link rel="stylesheet" href="/css/style.css">
			<script src="/js/app.js"></script>
			<script src="http://analytics.invalid/tracker.js"></script>
		</head><body>
			<p>Some content for the test page so it is readable enough</p>
			<img src="/img/ok.png">
			<img src="/img/broken.png">
		</body></html>`)
	})
	mux.HandleFunc("/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body { color: black; }")
	})
	mux.HandleFunc("/js/app.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		fmt.Fprint(w, "console.log('hi');")
	})
	mux.HandleFunc("/img/ok.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "fake-png-content")
	})
	mux.HandleFunc("/img/broken.png", func(w http.ResponseWriter, r *http.Request) {
		if broken["/img/broken.png"] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "fake-png-content")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	// Save the bookmark in the database
	bookmark := testutil.GetValidBookmark()
	bookmark.URL = server.URL
	saved, err := deps.Database().SaveBookmarks(ctx, true, *bookmark)
	require.NoError(t, err)
	require.Len(t, saved, 1)
	book := saved[0]

	// Download and process the bookmark to create the archive and manifest
	book.CreateArchive = true
	content, contentType, err := core.DownloadBookmark(server.URL)
	require.NoError(t, err)
	defer content.Close()

	processed, isFatal, err := core.ProcessBookmark(deps, core.ProcessRequest{
		DataDir:     cfg.Storage.DataDir,
		Bookmark:    book,
		Content:     content,
		ContentType: contentType,
	})
	require.NoError(t, err)
	require.False(t, isFatal)
	require.True(t, processed.HasArchive)

	return &archiveManifestTestFixture{
		cfg:    cfg,
		deps:   deps,
		book:   processed,
		broken: broken,
	}
}

func TestHandleGetArchiveManifest(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()

	t.Run("requires authentication", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleGetArchiveManifest,
			http.MethodGet,
			"/api/v1/bookmarks/1/archive/manifest",
			testutil.WithRequestPathValue("id", "1"),
		)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("invalid bookmark id", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleGetArchiveManifest,
			http.MethodGet,
			"/api/v1/bookmarks/invalid/archive/manifest",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", "invalid"),
		)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("bookmark not found", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleGetArchiveManifest,
			http.MethodGet,
			"/api/v1/bookmarks/999/archive/manifest",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", "999"),
		)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("bookmark without archive", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)

		bookmark := testutil.GetValidBookmark()
		saved, err := deps.Database().SaveBookmarks(ctx, true, *bookmark)
		require.NoError(t, err)
		require.Len(t, saved, 1)

		w := testutil.PerformRequest(
			deps,
			HandleGetArchiveManifest,
			http.MethodGet,
			"/api/v1/bookmarks/"+strconv.Itoa(saved[0].ID)+"/archive/manifest",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", strconv.Itoa(saved[0].ID)),
		)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("success with missing resources", func(t *testing.T) {
		fixture := newArchiveManifestTestFixture(t, ctx)
		id := strconv.Itoa(fixture.book.ID)

		w := testutil.PerformRequest(
			fixture.deps,
			HandleGetArchiveManifest,
			http.MethodGet,
			"/api/v1/bookmarks/"+id+"/archive/manifest",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", id),
		)
		require.Equal(t, http.StatusOK, w.Code)

		var response archiveManifestResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))

		assert.Equal(t, fixture.book.ID, response.BookmarkID)
		assert.Equal(t, 4, response.Total)
		assert.Equal(t, 2, response.OK)
		assert.Equal(t, 1, response.Missing)
		assert.Equal(t, 1, response.Skipped)

		// The analytics script must not be tracked
		for _, res := range response.Resources {
			assert.NotContains(t, res.URL, "analytics.invalid")
		}
	})
}

func TestHandleRetryArchiveResources(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()

	t.Run("requires authentication", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/1/archive/manifest/retry",
			testutil.WithRequestPathValue("id", "1"),
		)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("invalid bookmark id", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/invalid/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", "invalid"),
		)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("bookmark not found", func(t *testing.T) {
		_, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
		w := testutil.PerformRequest(
			deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/999/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", "999"),
		)
		require.Equal(t, http.StatusNotFound, w.Code)
	})

	t.Run("invalid json payload", func(t *testing.T) {
		fixture := newArchiveManifestTestFixture(t, ctx)
		id := strconv.Itoa(fixture.book.ID)

		w := testutil.PerformRequest(
			fixture.deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/"+id+"/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", id),
			testutil.WithBody("invalid json"),
		)
		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("retry all missing resources", func(t *testing.T) {
		fixture := newArchiveManifestTestFixture(t, ctx)
		id := strconv.Itoa(fixture.book.ID)

		// Fix the broken resource so the retry can succeed
		fixture.broken["/img/broken.png"] = false

		w := testutil.PerformRequest(
			fixture.deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/"+id+"/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", id),
			testutil.WithBody(`{}`),
		)
		require.Equal(t, http.StatusOK, w.Code)

		var response archiveManifestResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.Equal(t, 0, response.Missing)
		assert.Equal(t, 3, response.OK)
	})

	t.Run("retry specific resource", func(t *testing.T) {
		fixture := newArchiveManifestTestFixture(t, ctx)
		id := strconv.Itoa(fixture.book.ID)
		fixture.broken["/img/broken.png"] = false

		manifest, err := fixture.deps.Domains().Archiver().GetArchiveManifest(&fixture.book)
		require.NoError(t, err)
		missing := manifest.MissingResources()
		require.Len(t, missing, 1)

		payload, err := json.Marshal(map[string][]string{"urls": {missing[0].URL}})
		require.NoError(t, err)

		w := testutil.PerformRequest(
			fixture.deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/"+id+"/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", id),
			testutil.WithBody(string(payload)),
		)
		require.Equal(t, http.StatusOK, w.Code)

		var response archiveManifestResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.Equal(t, 0, response.Missing)
	})

	t.Run("retry keeps failing while resource is broken", func(t *testing.T) {
		fixture := newArchiveManifestTestFixture(t, ctx)
		id := strconv.Itoa(fixture.book.ID)

		w := testutil.PerformRequest(
			fixture.deps,
			HandleRetryArchiveResources,
			http.MethodPost,
			"/api/v1/bookmarks/"+id+"/archive/manifest/retry",
			testutil.WithFakeUser(),
			testutil.WithRequestPathValue("id", id),
			testutil.WithBody(`{}`),
		)
		require.Equal(t, http.StatusOK, w.Code)

		var response archiveManifestResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
		assert.Equal(t, 1, response.Missing)
	})
}
