package model

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strconv"
)

// Bookmark is the database representation of a bookmark
type Bookmark struct {
	ID         int    `db:"id"`
	URL        string `db:"url"`
	Title      string `db:"title"`
	Excerpt    string `db:"excerpt"`
	Author     string `db:"author"`
	Public     int    `db:"public"`
	CreatedAt  string `db:"created_at"`
	ModifiedAt string `db:"modified_at"`
	Content    string `db:"content" json:"-"`
	HTML       string `db:"html"    json:",omitempty"`
	HasContent bool   `db:"has_content"`
}

// BookmarkDTO is the data transfer object for bookmarks sent to/from clients
// It embeds the Bookmark struct and adds additional fields for API responses
type BookmarkDTO struct {
	Bookmark      `db:",inline"`
	Tags          []TagDTO
	ImageURL      string
	HasArchive    bool
	HasEbook      bool
	HasThumbnail  bool
	CreateArchive bool
	CreateEbook   bool
}

// PopulateImageURL sets the ImageURL field based on HasThumbnail
func (dto *BookmarkDTO) PopulateImageURL() {
	if dto.HasThumbnail {
		dto.ImageURL = GetThumbnailURL(dto)
	}
}

// ToBookmark extracts the embedded Bookmark from BookmarkDTO
func (dto *BookmarkDTO) ToBookmark() Bookmark {
	return dto.Bookmark
}

// ToDTO converts a Bookmark to a BookmarkDTO
func (b *Bookmark) ToDTO() BookmarkDTO {
	return BookmarkDTO{
		Bookmark: *b,
		Tags:     []TagDTO{},
	}
}

// GetTumnbailPath returns the relative path to the thumbnail of a bookmark in the filesystem
func GetThumbnailPath(bookmark *BookmarkDTO) string {
	return filepath.Join("thumb", strconv.Itoa(bookmark.ID))
}

// GetEbookPath returns the relative path to the ebook of a bookmark in the filesystem
func GetEbookPath(bookmark *BookmarkDTO) string {
	return filepath.Join("ebook", strconv.Itoa(bookmark.ID)+".epub")
}

// ArchiveFileName is the name of the archive file inside its directory.
const ArchiveFileName = "archive"

// GetArchiveDirPath returns the relative path to the directory that holds the
// archive of a bookmark. The directory is named after the hash of the bookmark
// URL, so bookmarks that point to the same URL share a single archive.
func GetArchiveDirPath(bookmark *BookmarkDTO) string {
	return filepath.Join("archive", GetArchiveHash(bookmark.URL))
}

// GetArchivePath returns the relative path to the archive file of a bookmark.
func GetArchivePath(bookmark *BookmarkDTO) string {
	return filepath.Join(GetArchiveDirPath(bookmark), ArchiveFileName)
}

// GetArchiveHash returns the name of the archive directory for the specified URL.
func GetArchiveHash(url string) string {
	sum := sha256.Sum256([]byte(url))
	return hex.EncodeToString(sum[:])
}

// ArchiveIsSharedBy reports whether any of the bookmarks points to the same
// URL, and therefore uses the same archive, as the specified URL.
func ArchiveIsSharedBy(url string, bookmarks []BookmarkDTO) bool {
	hash := GetArchiveHash(url)
	for _, bookmark := range bookmarks {
		if GetArchiveHash(bookmark.URL) == hash {
			return true
		}
	}
	return false
}

// GetThumbnailURL returns the URL path to the thumbnail of a bookmark
func GetThumbnailURL(bookmark *BookmarkDTO) string {
	return "/bookmark/" + strconv.Itoa(bookmark.ID) + "/thumb"
}

// GetEbookURL returns the URL path to the ebook of a bookmark
func GetEbookURL(bookmark *BookmarkDTO) string {
	return "/bookmark/" + strconv.Itoa(bookmark.ID) + "/ebook"
}

// GetArchiveURL returns the URL path to the archive of a bookmark
func GetArchiveURL(bookmark *BookmarkDTO) string {
	return "/bookmark/" + strconv.Itoa(bookmark.ID) + "/archive"
}
