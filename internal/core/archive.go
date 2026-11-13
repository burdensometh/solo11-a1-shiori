package core

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/warc"
	"github.com/spf13/afero"
)

// ErrInvalidArchive is returned when an archive file is missing, truncated or
// otherwise not a complete WARC archive.
var ErrInvalidArchive = errors.New("archive is incomplete or invalid")

// Values written by bbolt into the meta page of every database.
const (
	bboltMagic   uint32 = 0xED0CDAED
	bboltVersion uint32 = 2
)

// FindArchive returns the storage-relative path of a complete archive for the
// bookmark URL. Archives are stored in a directory named after the hash of the
// URL, so bookmarks pointing to the same URL share a single archive. Archives
// stored by older versions under the bookmark ID are also recognised.
func FindArchive(storage model.StorageDomain, bookmark *model.BookmarkDTO) (string, bool) {
	archivePath := model.GetArchivePath(bookmark)
	if fileIsValidArchive(storage, archivePath) {
		return archivePath, true
	}

	legacyPath := legacyArchivePath(bookmark)
	if fileIsValidArchive(storage, legacyPath) {
		return legacyPath, true
	}

	return "", false
}

// ArchiveExists reports whether a complete archive for the bookmark URL is
// already stored.
func ArchiveExists(deps model.Dependencies, bookmark *model.BookmarkDTO) bool {
	_, exists := FindArchive(deps.Domains().Storage(), bookmark)
	return exists
}

// legacyArchivePath is where versions without URL-based archive names stored
// the archive of a bookmark.
func legacyArchivePath(bookmark *model.BookmarkDTO) string {
	return filepath.Join("archive", strconv.Itoa(bookmark.ID))
}

func fileIsValidArchive(storage model.StorageDomain, path string) bool {
	if !storage.FileExists(path) {
		return false
	}

	file, err := storage.FS().Open(path)
	if err != nil {
		return false
	}
	defer file.Close()

	return validateArchiveFile(file) == nil
}

// validateArchiveFile performs a cheap check of a bbolt archive without
// opening the database. It detects empty and truncated files, which is what an
// interrupted write leaves behind.
func validateArchiveFile(file afero.File) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat archive: %w", err)
	}

	if info.Size() < 32 {
		return ErrInvalidArchive
	}

	var header [32]byte
	if _, err := file.ReadAt(header[:], 0); err != nil {
		return fmt.Errorf("%w: failed to read archive header: %v", ErrInvalidArchive, err)
	}

	magic := binary.NativeEndian.Uint32(header[16:20])
	version := binary.NativeEndian.Uint32(header[20:24])
	pageSize := binary.NativeEndian.Uint32(header[24:28])

	if magic != bboltMagic || version != bboltVersion || pageSize == 0 {
		return ErrInvalidArchive
	}

	// bbolt initializes every database with at least four pages. Anything
	// smaller has been truncated.
	if uint64(info.Size()) < uint64(pageSize)*4 {
		return fmt.Errorf("%w: size %d is smaller than four pages", ErrInvalidArchive, info.Size())
	}

	return nil
}

// validateArchiveOnDisk fully opens an archive file that lives on the OS
// filesystem and makes sure it contains the archived root resource.
func validateArchiveOnDisk(path string) error {
	archive, err := warc.Open(path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidArchive, err)
	}
	defer archive.Close()

	if !archive.HasResource("") {
		return fmt.Errorf("%w: archive has no root resource", ErrInvalidArchive)
	}

	return nil
}

// SaveBookmarkArchive stores the offline archive of a bookmark. If a complete
// archive for the bookmark URL already exists it is reused as is, so repeated
// saves don't duplicate it on disk. Otherwise the archive is built and
// validated in a temporary directory, which is then renamed into place. The
// final directory therefore either doesn't exist or holds a complete archive,
// and a failed save never damages a previously stored one.
func SaveBookmarkArchive(deps model.Dependencies, bookmark *model.BookmarkDTO, content io.Reader, contentType string, logArchival bool) error {
	storage := deps.Domains().Storage()

	if existingPath, exists := FindArchive(storage, bookmark); exists {
		return reuseArchive(deps, bookmark, existingPath)
	}

	// warc writes the archive through the OS filesystem, so build it in an
	// OS temporary file first and only move it into storage once validated.
	tmpFile, err := os.CreateTemp("", "archive")
	if err != nil {
		return fmt.Errorf("failed to create temp archive: %v", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to prepare temp archive: %v", err)
	}
	defer os.Remove(tmpPath)

	archivalRequest := warc.ArchivalRequest{
		URL:         bookmark.URL,
		Reader:      content,
		ContentType: contentType,
		UserAgent:   userAgent,
		LogEnabled:  logArchival,
	}

	if err := warc.NewArchive(archivalRequest, tmpPath); err != nil {
		return fmt.Errorf("failed to create archive: %v", err)
	}

	if err := validateArchiveOnDisk(tmpPath); err != nil {
		return fmt.Errorf("created archive failed validation: %w", err)
	}

	src, err := os.Open(tmpPath)
	if err != nil {
		return fmt.Errorf("failed to open temporary archive: %v", err)
	}
	defer src.Close()

	return installArchive(storage, bookmark, src)
}

// reuseArchive keeps a complete archive that is already stored. When the
// archive is still under the legacy bookmark-ID name it is moved to the
// URL-hash location, so it can be shared by every bookmark of that URL.
func reuseArchive(deps model.Dependencies, bookmark *model.BookmarkDTO, existingPath string) error {
	storage := deps.Domains().Storage()

	if existingPath == model.GetArchivePath(bookmark) {
		return nil
	}

	src, err := storage.FS().Open(existingPath)
	if err != nil {
		return fmt.Errorf("failed to open existing archive: %v", err)
	}
	defer src.Close()

	if err := installArchive(storage, bookmark, src); err != nil {
		return err
	}

	// The legacy copy is now duplicated in the URL-hash directory.
	if err := storage.FS().Remove(existingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		deps.Logger().WithError(err).WithField("path", existingPath).Warn("failed to remove legacy archive")
	}

	return nil
}

// removeIncompleteArchive deletes the archive directory at dstDir only if it
// doesn't hold a complete archive, so a concurrently installed archive is
// never destroyed.
func removeIncompleteArchive(storage model.StorageDomain, dstDir string) error {
	if fileIsValidArchive(storage, filepath.Join(dstDir, model.ArchiveFileName)) {
		return nil
	}

	if err := storage.FS().RemoveAll(dstDir); err != nil {
		return fmt.Errorf("failed to remove incomplete archive directory: %v", err)
	}

	return nil
}

// installArchive copies a validated archive into a temporary directory in
// storage and atomically renames that directory into its final hash-named
// location. On any failure only the temporary directory is removed.
func installArchive(storage model.StorageDomain, bookmark *model.BookmarkDTO, src io.Reader) error {
	fs := storage.FS()
	dstDir := model.GetArchiveDirPath(bookmark)
	parentDir := filepath.Dir(dstDir)

	if err := fs.MkdirAll(parentDir, model.DataDirPerm); err != nil {
		return fmt.Errorf("failed to create archive directory: %v", err)
	}

	tmpDir, err := afero.TempDir(fs, parentDir, ".tmp-"+model.GetArchiveHash(bookmark.URL)+"-")
	if err != nil {
		return fmt.Errorf("failed to create temporary archive directory: %v", err)
	}

	installed := false
	defer func() {
		if !installed {
			// A failed save must not touch anything but its temporary directory.
			_ = fs.RemoveAll(tmpDir)
		}
	}()

	dstPath := filepath.Join(tmpDir, model.ArchiveFileName)
	dst, err := fs.Create(dstPath)
	if err != nil {
		return fmt.Errorf("failed to create archive in temporary directory: %v", err)
	}

	if _, err := io.Copy(dst, src); err != nil {
		dst.Close()
		return fmt.Errorf("failed to write archive in temporary directory: %v", err)
	}

	if err := dst.Sync(); err != nil {
		dst.Close()
		return fmt.Errorf("failed to sync archive in temporary directory: %v", err)
	}

	if err := dst.Close(); err != nil {
		return fmt.Errorf("failed to close archive in temporary directory: %v", err)
	}

	installedFile, err := fs.Open(dstPath)
	if err != nil {
		return fmt.Errorf("failed to validate temporary archive: %v", err)
	}
	err = validateArchiveFile(installedFile)
	installedFile.Close()
	if err != nil {
		return fmt.Errorf("temporary archive failed validation: %w", err)
	}

	// If a complete archive appeared while this one was being built, keep it
	// and discard the temporary directory. Only the target location matters
	// here: a legacy archive may still be in place while it is migrated.
	if fileIsValidArchive(storage, model.GetArchivePath(bookmark)) {
		return nil
	}

	if err := fs.Rename(tmpDir, dstDir); err != nil {
		// The rename fails when the destination exists. A complete archive
		// installed by a concurrent writer must be kept; an incomplete one
		// left behind by an interrupted write is replaced.
		if fileIsValidArchive(storage, model.GetArchivePath(bookmark)) {
			return nil
		}

		if removeErr := removeIncompleteArchive(storage, dstDir); removeErr != nil {
			return removeErr
		}

		if err := fs.Rename(tmpDir, dstDir); err != nil {
			if fileIsValidArchive(storage, model.GetArchivePath(bookmark)) {
				return nil
			}
			return fmt.Errorf("failed to move archive to destination: %v", err)
		}
	}

	installed = true

	// Match the permissions used for the rest of the data directory; the
	// temporary directory was created with 0700.
	if err := fs.Chmod(dstDir, model.DataDirPerm); err != nil {
		return fmt.Errorf("failed to set archive directory permissions: %v", err)
	}

	return nil
}
