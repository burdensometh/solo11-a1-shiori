package model

import (
	"context"
	"io/fs"
	"os"
	"time"

	"github.com/go-shiori/warc"
	"github.com/spf13/afero"
)

// BookmarkSearchOrderMethod defines how search results should be ordered
type BookmarkSearchOrderMethod int

const (
	// DefaultSearchOrder is oldest to newest
	DefaultSearchOrder BookmarkSearchOrderMethod = iota
	// ByLastAddedSearchOrder is from newest addition to the oldest
	ByLastAddedSearchOrder
	// ByLastModifiedSearchOrder is from latest modified to the oldest
	ByLastModifiedSearchOrder
)

// BookmarksSearchOptions represents domain-level options for searching bookmarks
type BookmarksSearchOptions struct {
	IDs          []int
	Tags         []string
	ExcludedTags []string
	Keyword      string
	WithContent  bool
	OrderMethod  BookmarkSearchOrderMethod
	Limit        int
	Offset       int
}

// ToDBGetBookmarksOptions converts domain search options to database options
func (opts BookmarksSearchOptions) ToDBGetBookmarksOptions() DBGetBookmarksOptions {
	return DBGetBookmarksOptions{
		IDs:          opts.IDs,
		Tags:         opts.Tags,
		ExcludedTags: opts.ExcludedTags,
		Keyword:      opts.Keyword,
		WithContent:  opts.WithContent,
		OrderMethod:  DBOrderMethod(opts.OrderMethod),
		Limit:        opts.Limit,
		Offset:       opts.Offset,
	}
}

type BookmarksDomain interface {
	HasEbook(b *BookmarkDTO) bool
	HasArchive(b *BookmarkDTO) bool
	HasThumbnail(b *BookmarkDTO) bool
	GetBookmark(ctx context.Context, id DBID) (*BookmarkDTO, error)
	GetBookmarks(ctx context.Context, ids []int) ([]BookmarkDTO, error)
	SearchBookmarks(ctx context.Context, options BookmarksSearchOptions) ([]BookmarkDTO, error)
	CountBookmarks(ctx context.Context, options BookmarksSearchOptions) (int, error)
	UpdateBookmarkCache(ctx context.Context, bookmark BookmarkDTO, keepMetadata bool, skipExist bool) (*BookmarkDTO, error)
	// IngestBookmark is the single entry point used by every client (web UI,
	// browser extension, legacy API) to create or update a bookmark together with
	// its readable content, thumbnail, archive and ebook. It makes sure the
	// archive/title/processing-result semantics are identical across entry points.
	IngestBookmark(ctx context.Context, request IngestBookmarkRequest) (*BookmarkDTO, error)
	BulkUpdateBookmarkTags(ctx context.Context, bookmarkIDs []int, tagIDs []int) error
	AddTagToBookmark(ctx context.Context, bookmarkID int, tagID int) error
	RemoveTagFromBookmark(ctx context.Context, bookmarkID int, tagID int) error
	BookmarkExists(ctx context.Context, id int) (bool, error)
	CreateBookmark(ctx context.Context, bookmark Bookmark) (*BookmarkDTO, error)
	UpdateBookmark(ctx context.Context, bookmark Bookmark) (*BookmarkDTO, error)
	DeleteBookmarks(ctx context.Context, ids []int) error
}

// IngestBookmarkRequest is the payload for BookmarksDomain.IngestBookmark.
// Content is optional: the browser extension can send the page HTML itself,
// when it is nil the page is downloaded from the URL.
type IngestBookmarkRequest struct {
	URL           string
	Title         string
	Excerpt       string
	Public        int
	Tags          []TagDTO
	Content       []byte
	ContentType   string
	CreateArchive bool
	CreateEbook   bool
	// UpdateIfExists defines what happens when a bookmark with the same URL
	// already exists. When false (web API behavior) the call fails with
	// ErrBookmarkAlreadyExists. When true (extension behavior) the existing
	// bookmark is reused and reprocessed.
	UpdateIfExists bool
}

type AuthDomain interface {
	CheckToken(ctx context.Context, userJWT string) (*AccountDTO, error)
	GetAccountFromCredentials(ctx context.Context, username, password string) (*AccountDTO, error)
	CreateTokenForAccount(account *AccountDTO, expiration time.Time) (string, error)
}

type AccountsDomain interface {
	ListAccounts(ctx context.Context) ([]AccountDTO, error)
	GetAccountByUsername(ctx context.Context, username string) (*AccountDTO, error)
	GetAccountByID(ctx context.Context, id DBID) (*AccountDTO, error)
	CreateAccount(ctx context.Context, account AccountDTO) (*AccountDTO, error)
	UpdateAccount(ctx context.Context, account AccountDTO) (*AccountDTO, error)
	DeleteAccount(ctx context.Context, id int) error
}

type ArchiverDomain interface {
	DownloadBookmarkArchive(book BookmarkDTO) (*BookmarkDTO, error)
	GetBookmarkArchive(book *BookmarkDTO) (*warc.Archive, error)
}

type StorageDomain interface {
	Stat(name string) (fs.FileInfo, error)
	FS() afero.Fs
	FileExists(path string) bool
	DirExists(path string) bool
	WriteData(dst string, data []byte) error
	WriteFile(dst string, src *os.File) error
}

type TagsDomain interface {
	ListTags(ctx context.Context, opts ListTagsOptions) ([]TagDTO, error)
	CountTags(ctx context.Context, opts ListTagsOptions) (int, error)
	CreateTag(ctx context.Context, tag TagDTO) (TagDTO, error)
	GetTag(ctx context.Context, id int) (TagDTO, error)
	UpdateTag(ctx context.Context, tag TagDTO) (TagDTO, error)
	DeleteTag(ctx context.Context, id int) error
	TagExists(ctx context.Context, id int) (bool, error)
}
