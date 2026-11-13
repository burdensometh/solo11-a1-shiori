package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-shiori/warc"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// BuildTestArchive returns the bytes of a complete, valid archive of a small
// HTML page, as written by the warc package.
func BuildTestArchive(t *testing.T) []byte {
	t.Helper()

	tmpPath := filepath.Join(t.TempDir(), "archive")
	require.NoError(t, warc.NewArchive(warc.ArchivalRequest{
		URL:         "https://example.com",
		Reader:      strings.NewReader("<html><body><p>test archive</p></body></html>"),
		ContentType: "text/html",
	}, tmpPath))

	data, err := os.ReadFile(tmpPath)
	require.NoError(t, err)

	return data
}

// WriteTestArchive writes a complete archive at path in fs, creating parent
// directories as needed.
func WriteTestArchive(t *testing.T, fs afero.Fs, path string) {
	t.Helper()

	require.NoError(t, fs.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, afero.WriteFile(fs, path, BuildTestArchive(t), 0644))
}
