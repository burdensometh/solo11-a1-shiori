package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestArchiveManifestCountByStatus(t *testing.T) {
	manifest := &ArchiveManifest{
		Resources: []ArchiveResource{
			{URL: "https://example.com/a.png", Status: ArchiveResourceOK},
			{URL: "https://example.com/b.png", Status: ArchiveResourceFailed},
			{URL: "https://example.com/c.css", Status: ArchiveResourceOK},
			{URL: "https://example.com/d.js", Status: ArchiveResourceFailed},
			{URL: "https://example.com/e.js", Status: ArchiveResourceFailed},
			{URL: "https://example.com/f.js", Status: ArchiveResourceSkipped},
		},
	}

	ok, failed, skipped := manifest.CountByStatus()
	assert.Equal(t, 2, ok)
	assert.Equal(t, 3, failed)
	assert.Equal(t, 1, skipped)
}

func TestArchiveManifestMissingResources(t *testing.T) {
	manifest := &ArchiveManifest{
		Resources: []ArchiveResource{
			{URL: "https://example.com/a.png", Status: ArchiveResourceOK},
			{URL: "https://example.com/b.png", Status: ArchiveResourceFailed},
			{URL: "https://example.com/c.css", Status: ArchiveResourceFailed},
		},
	}

	t.Run("all missing", func(t *testing.T) {
		missing := manifest.MissingResources()
		require.Len(t, missing, 2)
		assert.Equal(t, "https://example.com/b.png", missing[0].URL)
		assert.Equal(t, "https://example.com/c.css", missing[1].URL)
	})

	t.Run("filtered by url", func(t *testing.T) {
		missing := manifest.MissingResources("https://example.com/c.css")
		require.Len(t, missing, 1)
		assert.Equal(t, "https://example.com/c.css", missing[0].URL)
	})

	t.Run("filtered by url without failures", func(t *testing.T) {
		missing := manifest.MissingResources("https://example.com/a.png")
		assert.Empty(t, missing)
	})

	t.Run("filtered by unknown url", func(t *testing.T) {
		missing := manifest.MissingResources("https://example.com/unknown.png")
		assert.Empty(t, missing)
	})
}

func TestGetArchiveManifestPath(t *testing.T) {
	bookmark := &BookmarkDTO{Bookmark: Bookmark{ID: 42}}
	assert.Equal(t, "archive/42.manifest.json", GetArchiveManifestPath(bookmark))
}
