package domains_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-shiori/shiori/internal/core"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/shiori/internal/testutil"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiverDomainManifest(t *testing.T) {
	logger := logrus.New()
	ctx := context.Background()
	cfg, deps := testutil.GetTestConfigurationAndDependencies(t, ctx, logger)
	cfg.Archive.ExcludedDomains = []string{"analytics.invalid"}

	broken := map[string]bool{"/img/broken.png": true}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<html><head>
			<link rel="stylesheet" href="/css/style.css">
		</head><body>
			<img src="/img/ok.png">
			<img src="/img/broken.png">
		</body></html>`)
	})
	mux.HandleFunc("/css/style.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css")
		fmt.Fprint(w, "body{}")
	})
	mux.HandleFunc("/img/ok.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "png")
	})
	mux.HandleFunc("/img/broken.png", func(w http.ResponseWriter, r *http.Request) {
		if broken["/img/broken.png"] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "png")
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	book := model.BookmarkDTO{
		Bookmark:      model.Bookmark{ID: 1, URL: server.URL, Title: "Test"},
		CreateArchive: true,
	}

	// Manifest does not exist before archiving
	_, err := deps.Domains().Archiver().GetArchiveManifest(&book)
	assert.ErrorIs(t, err, core.ErrArchiveManifestNotFound)

	// Archive the bookmark through the domain, which also builds the manifest
	processed, err := deps.Domains().Archiver().DownloadBookmarkArchive(book)
	require.NoError(t, err)
	require.True(t, processed.HasArchive)

	manifest, err := deps.Domains().Archiver().GetArchiveManifest(processed)
	require.NoError(t, err)
	ok, failed, _ := manifest.CountByStatus()
	assert.Equal(t, 2, ok)
	assert.Equal(t, 1, failed)

	// Fix the broken resource and retry it through the domain
	broken["/img/broken.png"] = false

	manifest, err = deps.Domains().Archiver().RetryArchiveResources(processed, nil)
	require.NoError(t, err)
	ok, failed, _ = manifest.CountByStatus()
	assert.Equal(t, 3, ok)
	assert.Equal(t, 0, failed)
}
