package model

import (
	"path/filepath"
	"strconv"
)

// ArchiveResourceType classifies the resources referenced by an archived page.
type ArchiveResourceType string

const (
	ArchiveResourceImage  ArchiveResourceType = "image"
	ArchiveResourceStyle  ArchiveResourceType = "style"
	ArchiveResourceScript ArchiveResourceType = "script"
)

// ArchiveResourceStatus is the archival state of a single resource.
type ArchiveResourceStatus string

const (
	ArchiveResourceOK     ArchiveResourceStatus = "ok"
	ArchiveResourceFailed ArchiveResourceStatus = "failed"
	// ArchiveResourceSkipped marks resources that the archiver does not store
	// by design (scripts are stripped from archived pages), so they are not
	// counted as missing.
	ArchiveResourceSkipped ArchiveResourceStatus = "skipped"
)

// ArchiveResource is a single entry of the archive manifest: a resource
// referenced by the page (image, stylesheet or script) and whether it was
// successfully stored in the archive.
type ArchiveResource struct {
	URL    string                `json:"url"`
	Name   string                `json:"name"`
	Type   ArchiveResourceType   `json:"type"`
	Status ArchiveResourceStatus `json:"status"`
	Error  string                `json:"error,omitempty"`
}

// ArchiveManifest records every resource a page referenced when it was
// archived, so it's possible to know if the archive is complete and which
// resources are missing.
type ArchiveManifest struct {
	BookmarkID int               `json:"bookmark_id"`
	URL        string            `json:"url"`
	CreatedAt  string            `json:"created_at"`
	Resources  []ArchiveResource `json:"resources"`
}

// CountByStatus returns how many resources are archived correctly, how many
// are missing from the archive and how many were skipped by design.
func (m *ArchiveManifest) CountByStatus() (ok int, failed int, skipped int) {
	for _, res := range m.Resources {
		switch res.Status {
		case ArchiveResourceOK:
			ok++
		case ArchiveResourceFailed:
			failed++
		case ArchiveResourceSkipped:
			skipped++
		}
	}
	return ok, failed, skipped
}

// MissingResources returns the resources that failed to be archived,
// optionally filtered to the submitted URLs. An empty filter returns all
// missing resources.
func (m *ArchiveManifest) MissingResources(urls ...string) []ArchiveResource {
	var filter map[string]struct{}
	if len(urls) > 0 {
		filter = make(map[string]struct{}, len(urls))
		for _, u := range urls {
			filter[u] = struct{}{}
		}
	}

	missing := []ArchiveResource{}
	for _, res := range m.Resources {
		if res.Status != ArchiveResourceFailed {
			continue
		}
		if filter != nil {
			if _, ok := filter[res.URL]; !ok {
				continue
			}
		}
		missing = append(missing, res)
	}
	return missing
}

// GetArchiveManifestPath returns the relative path to the archive manifest
// of a bookmark in the filesystem.
func GetArchiveManifestPath(bookmark *BookmarkDTO) string {
	return filepath.Join("archive", strconv.Itoa(bookmark.ID)+".manifest.json")
}
