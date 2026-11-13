package core_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	fp "path/filepath"
	"strings"
	"testing"

	"github.com/go-shiori/shiori/internal/config"
	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/go-shiori/warc"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveResourceName(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{
			url:      "https://example.com/img/photo.png",
			expected: "https-example.com-img-photo.png",
		},
		{
			url:      "http://example.com/css/style.css?v=1&lang=en",
			expected: "http-example.com-css-style.css-v=1&lang=en",
		},
		{
			url:      "https://example.com/js/app.js#section",
			expected: "https-example.com-js-app.js-section",
		},
		{
			// escaped query characters are unescaped, like the archiver does
			url:      "https://example.com/load.php?modules=ext.styles%7Cext.cite",
			expected: "https-example.com-load.php-modules=ext.styles|ext.cite",
		},
		{
			url:      "https://example.com:8080/img/a+b.png",
			expected: "https-example.com-8080-img-a+b.png",
		},
	}

	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			assert.Equal(t, test.expected, core.ArchiveResourceName(test.url))
		})
	}
}

func TestExtractArchiveResources(t *testing.T) {
	excludedDomains := []string{"google-analytics.com", "hm.baidu.com"}

	t.Run("extracts images styles and scripts", func(t *testing.T) {
		html := `<html><head>
			<link rel="stylesheet" href="/css/main.css">
			<link rel="icon" type="image/png" href="/favicon.png">
			<script src="/js/app.js"></script>
		</head><body>
			<img src="/img/photo.png">
			<img srcset="/img/small.png 480w, /img/large.png 1024w">
			<video poster="/img/poster.jpg" src="/video/movie.mp4"></video>
			<picture><source srcset="/img/hero.webp"></picture>
		</body></html>`

		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com/page", excludedDomains)
		require.NoError(t, err)

		byURL := map[string]model.ArchiveResourceType{}
		for _, res := range resources {
			byURL[res.URL] = res.Type
		}

		assert.Equal(t, map[string]model.ArchiveResourceType{
			"https://example.com/css/main.css":   model.ArchiveResourceStyle,
			"https://example.com/favicon.png":    model.ArchiveResourceImage,
			"https://example.com/js/app.js":      model.ArchiveResourceScript,
			"https://example.com/img/photo.png":  model.ArchiveResourceImage,
			"https://example.com/img/small.png":  model.ArchiveResourceImage,
			"https://example.com/img/large.png":  model.ArchiveResourceImage,
			"https://example.com/img/poster.jpg": model.ArchiveResourceImage,
			"https://example.com/img/hero.webp":  model.ArchiveResourceImage,
		}, byURL)
	})

	t.Run("resolves relative urls against the page url", func(t *testing.T) {
		html := `<img src="img/photo.png"><link rel="stylesheet" href="../css/main.css">`
		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com/blog/post/", nil)
		require.NoError(t, err)
		require.Len(t, resources, 2)
		assert.Equal(t, "https://example.com/blog/post/img/photo.png", resources[0].URL)
		assert.Equal(t, "https://example.com/blog/css/main.css", resources[1].URL)
	})

	t.Run("removes fragment and tracking queries", func(t *testing.T) {
		html := `<img src="https://example.com/img/photo.png?utm_source=x&utm_medium=y&keep=1#frag">`
		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", nil)
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, "https://example.com/img/photo.png?keep=1", resources[0].URL)
	})

	t.Run("deduplicates resources", func(t *testing.T) {
		html := `<img src="/a.png"><img src="/a.png"><img srcset="/a.png 1x, /a.png 2x">`
		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", nil)
		require.NoError(t, err)
		assert.Len(t, resources, 1)
	})

	t.Run("excludes third party analytics domains and subdomains", func(t *testing.T) {
		html := `<html><head>
			<script src="https://www.google-analytics.com/analytics.js"></script>
			<script src="https://hm.baidu.com/hm.js?abc"></script>
			<script src="https://example.com/js/app.js"></script>
		</head><body>
			<img src="https://google-analytics.com/pixel.gif">
		</body></html>`

		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", excludedDomains)
		require.NoError(t, err)
		require.Len(t, resources, 1)
		assert.Equal(t, "https://example.com/js/app.js", resources[0].URL)
	})

	t.Run("skips non http urls", func(t *testing.T) {
		html := `<img src="data:image/png;base64,iVBORw0KGgo=">
			<img src="javascript:void(0)">
			<img src="#fragment">
			<img src="  ">`
		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", nil)
		require.NoError(t, err)
		assert.Empty(t, resources)
	})

	t.Run("extracts lazy loaded images from data attributes", func(t *testing.T) {
		html := `<img data-src="/img/lazy.png" class="lazyload">
			<img src="/img/regular.png" data-src="/img/ignored.png">`
		resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", nil)
		require.NoError(t, err)
		require.Len(t, resources, 2)
		assert.Equal(t, "https://example.com/img/lazy.png", resources[0].URL)
		assert.Equal(t, "https://example.com/img/regular.png", resources[1].URL)
	})

	t.Run("fails with invalid page url", func(t *testing.T) {
		_, err := core.ExtractArchiveResources([]byte("<html></html>"), "not-an-url", nil)
		assert.Error(t, err)
	})
}

// archiveTestServer returns an httptest server serving a page with several
// resources, some of them failing. The broken resources can be fixed to test
// the retry.
func archiveTestServer(t *testing.T, broken *map[string]bool) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head>
			<link rel="stylesheet" href="/css/style.css">
			<script src="/js/app.js"></script>
			<script src="http://analytics.invalid/tracker.js"></script>
		</head><body>
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
		if (*broken)["/img/broken.png"] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "fake-png-content")
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func TestBuildSaveLoadAndRetryArchiveManifest(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()
	cfg, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
	cfg.Archive.ExcludedDomains = []string{"analytics.invalid"}

	broken := map[string]bool{"/img/broken.png": true}
	server := archiveTestServer(t, &broken)

	// Create a real archive of the test page
	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: server.URL}}
	archivePath := fp.Join(cfg.Storage.DataDir, model.GetArchivePath(book))

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	err = warc.NewArchive(warc.ArchivalRequest{
		URL:         book.URL,
		Reader:      resp.Body,
		ContentType: "text/html",
	}, archivePath)
	require.NoError(t, err)

	// Read the page HTML again to build the manifest
	resp2, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp2.Body.Close()
	htmlContent, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)

	// Build the manifest: the broken image must be marked as failed, the
	// analytics script must not be tracked at all
	manifest, err := core.BuildArchiveManifest(deps, book, htmlContent, archivePath)
	require.NoError(t, err)

	byURL := map[string]model.ArchiveResource{}
	for _, res := range manifest.Resources {
		byURL[res.URL] = res
	}

	require.Len(t, manifest.Resources, 4, "analytics script must be excluded from the manifest")
	assert.NotContains(t, byURL, "http://analytics.invalid/tracker.js")

	assert.Equal(t, model.ArchiveResourceOK, byURL[server.URL+"/css/style.css"].Status)
	assert.Equal(t, model.ArchiveResourceOK, byURL[server.URL+"/img/ok.png"].Status)

	// Scripts are stripped from archives by design, so they are marked as
	// skipped instead of missing
	scriptRes := byURL[server.URL+"/js/app.js"]
	assert.Equal(t, model.ArchiveResourceSkipped, scriptRes.Status)

	brokenRes := byURL[server.URL+"/img/broken.png"]
	assert.Equal(t, model.ArchiveResourceFailed, brokenRes.Status)
	assert.NotEmpty(t, brokenRes.Error)

	ok, failed, skipped := manifest.CountByStatus()
	assert.Equal(t, 2, ok)
	assert.Equal(t, 1, failed)
	assert.Equal(t, 1, skipped)

	// Save and load the manifest
	require.NoError(t, core.SaveArchiveManifest(deps, manifest))

	loaded, err := core.LoadArchiveManifest(deps, book)
	require.NoError(t, err)
	assert.Equal(t, manifest.BookmarkID, loaded.BookmarkID)
	assert.Equal(t, len(manifest.Resources), len(loaded.Resources))

	// Retry while the resource is still broken: it stays failed
	manifest, err = core.RetryArchiveResources(deps, book, nil)
	require.NoError(t, err)
	_, failed, _ = manifest.CountByStatus()
	assert.Equal(t, 1, failed)

	// Fix the resource and retry: the manifest must be complete now
	broken["/img/broken.png"] = false

	manifest, err = core.RetryArchiveResources(deps, book, nil)
	require.NoError(t, err)
	ok, failed, skipped = manifest.CountByStatus()
	assert.Equal(t, 3, ok)
	assert.Equal(t, 0, failed)
	assert.Equal(t, 1, skipped)

	// The recovered resource must now be served from the archive
	archive, err := warc.Open(archivePath)
	require.NoError(t, err)
	defer archive.Close()

	name := core.ArchiveResourceName(server.URL + "/img/broken.png")
	require.True(t, archive.HasResource(name))
	content, contentType, err := archive.Read(name)
	require.NoError(t, err)
	assert.Contains(t, contentType, "image/png")
	assert.NotEmpty(t, content)

	// The saved manifest must reflect the retry result
	loaded, err = core.LoadArchiveManifest(deps, book)
	require.NoError(t, err)
	_, failed, _ = loaded.CountByStatus()
	assert.Equal(t, 0, failed)
}

func TestRetryArchiveResourcesWithURLFilter(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()
	cfg, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
	cfg.Archive.ExcludedDomains = []string{"analytics.invalid"}

	broken := map[string]bool{"/img/broken.png": true}
	server := archiveTestServer(t, &broken)

	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 1, URL: server.URL}}
	archivePath := fp.Join(cfg.Storage.DataDir, model.GetArchivePath(book))

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.NoError(t, warc.NewArchive(warc.ArchivalRequest{
		URL:         book.URL,
		Reader:      resp.Body,
		ContentType: "text/html",
	}, archivePath))

	manifest := &model.ArchiveManifest{
		BookmarkID: book.ID,
		URL:        book.URL,
		Resources: []model.ArchiveResource{
			{URL: server.URL + "/img/broken.png", Name: core.ArchiveResourceName(server.URL + "/img/broken.png"), Type: model.ArchiveResourceImage, Status: model.ArchiveResourceFailed, Error: "missing"},
			{URL: server.URL + "/img/other.png", Name: core.ArchiveResourceName(server.URL + "/img/other.png"), Type: model.ArchiveResourceImage, Status: model.ArchiveResourceFailed, Error: "missing"},
		},
	}
	require.NoError(t, core.SaveArchiveManifest(deps, manifest))

	// Only the submitted URL is retried
	broken["/img/broken.png"] = false
	updated, err := core.RetryArchiveResources(deps, book, []string{server.URL + "/img/broken.png"})
	require.NoError(t, err)

	byURL := map[string]model.ArchiveResource{}
	for _, res := range updated.Resources {
		byURL[res.URL] = res
	}
	assert.Equal(t, model.ArchiveResourceOK, byURL[server.URL+"/img/broken.png"].Status)
	assert.Equal(t, model.ArchiveResourceFailed, byURL[server.URL+"/img/other.png"].Status)
}

func TestLoadArchiveManifestNotFound(t *testing.T) {
	logger := logrus.New()
	_, deps := testutil.GetTestConfigurationAndDependencies(t, context.Background(), logger)

	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 99}}
	_, err := core.LoadArchiveManifest(deps, book)
	assert.ErrorIs(t, err, core.ErrArchiveManifestNotFound)
}

func TestRetryArchiveResourcesWithoutManifest(t *testing.T) {
	logger := logrus.New()
	_, deps := testutil.GetTestConfigurationAndDependencies(t, context.Background(), logger)

	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: 99}}
	_, err := core.RetryArchiveResources(deps, book, nil)
	assert.ErrorIs(t, err, core.ErrArchiveManifestNotFound)
}

func TestProcessBookmarkCreatesManifest(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()
	cfg, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
	cfg.Archive.ExcludedDomains = []string{"analytics.invalid"}

	broken := map[string]bool{"/img/broken.png": true}
	server := archiveTestServer(t, &broken)

	resp, err := http.Get(server.URL)
	require.NoError(t, err)
	defer resp.Body.Close()

	book := model.BookmarkDTO{
		Bookmark: model.Bookmark{
			ID:    1,
			URL:   server.URL,
			Title: "Test",
		},
		CreateArchive: true,
	}

	processed, isFatal, err := core.ProcessBookmark(deps, core.ProcessRequest{
		DataDir:     cfg.Storage.DataDir,
		Bookmark:    book,
		Content:     resp.Body,
		ContentType: "text/html",
	})
	require.NoError(t, err)
	require.False(t, isFatal)
	require.True(t, processed.HasArchive)

	// The manifest must have been created along with the archive
	manifest, err := core.LoadArchiveManifest(deps, &processed)
	require.NoError(t, err)
	assert.Equal(t, processed.ID, manifest.BookmarkID)
	assert.Len(t, manifest.Resources, 4)

	ok, failed, skipped := manifest.CountByStatus()
	assert.Equal(t, 2, ok)
	assert.Equal(t, 1, failed)
	assert.Equal(t, 1, skipped)
}

func TestExtractArchiveResourcesDefaultExclusions(t *testing.T) {
	// The default exclusion list must cover the usual analytics providers
	html := `<html><head>
		<script src="https://www.google-analytics.com/analytics.js"></script>
		<script src="https://www.googletagmanager.com/gtm.js"></script>
		<script src="https://connect.facebook.net/en_US/fbevents.js"></script>
		<script src="https://hm.baidu.com/hm.js"></script>
		<script src="/js/app.js"></script>
	</head></html>`

	resources, err := core.ExtractArchiveResources([]byte(html), "https://example.com", config.DefaultArchiveExcludedDomains)
	require.NoError(t, err)
	require.Len(t, resources, 1)
	assert.True(t, strings.HasSuffix(resources[0].URL, "/js/app.js"))
}
