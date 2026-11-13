package core

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	nurl "net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/go-shiori/shiori/internal/config"
	"github.com/go-shiori/shiori/internal/model"
	"github.com/go-shiori/warc"
	"github.com/spf13/afero"
	"go.etcd.io/bbolt"
)

var (
	rxArchiveHTTPScheme    = regexp.MustCompile(`(?i)^https?:\/{2}`)
	rxArchiveRepeatedStrip = regexp.MustCompile(`(?i)-+`)
	rxArchiveTrailingSlash = regexp.MustCompile(`(?i)/+$`)
)

// ErrArchiveManifestNotFound is returned when a bookmark has no archive manifest.
var ErrArchiveManifestNotFound = fmt.Errorf("archive manifest not found")

// ArchiveResourceName converts a resource URL to the name used to store it
// inside the archive. It mirrors the naming scheme used by the warc archiver
// so manifest entries can be checked against the archived content.
func ArchiveResourceName(rawURL string) string {
	resourceName := rawURL

	// Unescape query and path, since browsers register the unescaped URL
	if tmp, err := nurl.Parse(rawURL); err == nil {
		if unescapedQuery, _ := nurl.QueryUnescape(tmp.RawQuery); unescapedQuery != "" {
			tmp.RawQuery = unescapedQuery
		}

		resourceName = tmp.String()
		resourceName = strings.Replace(resourceName, tmp.EscapedPath(), tmp.Path, 1)
	}

	resourceName = strings.ReplaceAll(resourceName, "://", "/")
	resourceName = strings.ReplaceAll(resourceName, ":", "-")
	resourceName = strings.ReplaceAll(resourceName, "?", "-")
	resourceName = strings.ReplaceAll(resourceName, "#", "-")
	resourceName = strings.ReplaceAll(resourceName, "/", "-")
	resourceName = strings.ReplaceAll(resourceName, " ", "-")
	resourceName = rxArchiveRepeatedStrip.ReplaceAllString(resourceName, "-")

	return resourceName
}

// normalizeArchiveResourceURL replicates the URL normalization done by the
// archiver before storing a resource: resolve against the page URL, remove
// fragments and tracking queries, strip trailing slashes and encode spaces.
func normalizeArchiveResourceURL(rawURL string, pageURL *nurl.URL) (string, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || strings.Contains(rawURL, ":") && !rxArchiveHTTPScheme.MatchString(rawURL) {
		return "", fmt.Errorf("invalid url")
	}

	// Resolve relative URLs against the page URL
	tmp, err := nurl.Parse(rawURL)
	if err != nil {
		return "", err
	}
	resolved := pageURL.ResolveReference(tmp)

	// Remove fragment and tracking queries
	queries := resolved.Query()
	for key := range queries {
		if strings.HasPrefix(key, "utm_") {
			queries.Del(key)
		}
	}
	resolved.Fragment = ""
	resolved.RawQuery = queries.Encode()

	normalized := resolved.String()
	normalized = rxArchiveTrailingSlash.ReplaceAllString(normalized, "")
	normalized = strings.ReplaceAll(normalized, " ", "+")

	return normalized, nil
}

// isExcludedArchiveDomain reports whether the host belongs to one of the
// excluded domains or any of their subdomains.
func isExcludedArchiveDomain(host string, excludedDomains []string) bool {
	host = strings.ToLower(host)
	for _, domain := range excludedDomains {
		domain = strings.ToLower(strings.TrimSpace(domain))
		if domain == "" {
			continue
		}
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// ExtractArchiveResources parses the page HTML and returns the list of
// resources it references (images, stylesheets and scripts), with URLs
// normalized the same way the archiver does. Resources hosted on excluded
// domains (third-party analytics) are skipped.
func ExtractArchiveResources(htmlContent []byte, pageURL string, excludedDomains []string) ([]model.ArchiveResource, error) {
	parsedURL, err := nurl.ParseRequestURI(pageURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Hostname() == "" {
		return nil, fmt.Errorf("url %q is not valid", pageURL)
	}

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %v", err)
	}

	resources := []model.ArchiveResource{}
	seen := make(map[string]struct{})

	addResource := func(rawURL string, resType model.ArchiveResourceType) {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" || strings.HasPrefix(rawURL, "#") {
			return
		}

		normalized, err := normalizeArchiveResourceURL(rawURL, parsedURL)
		if err != nil {
			return
		}

		if u, err := nurl.Parse(normalized); err != nil || isExcludedArchiveDomain(u.Hostname(), excludedDomains) {
			return
		}

		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}

		resources = append(resources, model.ArchiveResource{
			URL:  normalized,
			Name: ArchiveResourceName(normalized),
			Type: resType,
		})
	}

	// Images: <img src>, <img srcset>, <source src>, <source srcset>,
	// <video poster> and favicons
	addFromSrcset := func(srcset string) {
		for _, candidate := range strings.Split(srcset, ",") {
			parts := strings.Fields(strings.TrimSpace(candidate))
			if len(parts) > 0 {
				addResource(parts[0], model.ArchiveResourceImage)
			}
		}
	}

	doc.Find("img").Each(func(_ int, s *goquery.Selection) {
		src, hasSrc := s.Attr("src")
		srcset, hasSrcset := s.Attr("srcset")

		// Lazy loaded images keep their URL in data attributes
		if !hasSrc && !hasSrcset {
			src, hasSrc = s.Attr("data-src")
			srcset, hasSrcset = s.Attr("data-srcset")
		}

		if hasSrc {
			addResource(src, model.ArchiveResourceImage)
		}
		if hasSrcset {
			addFromSrcset(srcset)
		}
	})

	doc.Find("source").Each(func(_ int, s *goquery.Selection) {
		src, hasSrc := s.Attr("src")
		srcset, hasSrcset := s.Attr("srcset")

		if !hasSrc && !hasSrcset {
			src, hasSrc = s.Attr("data-src")
			srcset, hasSrcset = s.Attr("data-srcset")
		}

		if hasSrc {
			addResource(src, model.ArchiveResourceImage)
		}
		if hasSrcset {
			addFromSrcset(srcset)
		}
	})

	doc.Find("video[poster]").Each(func(_ int, s *goquery.Selection) {
		if poster, ok := s.Attr("poster"); ok {
			addResource(poster, model.ArchiveResourceImage)
		}
	})

	doc.Find("link[href]").Each(func(_ int, s *goquery.Selection) {
		rel := strings.ToLower(strings.TrimSpace(s.AttrOr("rel", "")))
		href, _ := s.Attr("href")
		switch {
		case strings.Contains(rel, "stylesheet"):
			addResource(href, model.ArchiveResourceStyle)
		case strings.Contains(rel, "icon"):
			addResource(href, model.ArchiveResourceImage)
		}
	})

	// Scripts
	doc.Find("script[src]").Each(func(_ int, s *goquery.Selection) {
		if src, ok := s.Attr("src"); ok {
			addResource(src, model.ArchiveResourceScript)
		}
	})

	return resources, nil
}

// archiveExcludedDomains returns the configured list of excluded domains,
// falling back to the default list of third-party analytics domains.
func archiveExcludedDomains(deps model.Dependencies) []string {
	cfg := deps.Config()
	if cfg != nil && cfg.Archive != nil && len(cfg.Archive.ExcludedDomains) > 0 {
		return cfg.Archive.ExcludedDomains
	}
	return config.DefaultArchiveExcludedDomains
}

// BuildArchiveManifest extracts the resources referenced by the page and
// checks each one against the archive, marking it as archived or missing.
func BuildArchiveManifest(deps model.Dependencies, book *model.BookmarkDTO, htmlContent []byte, archivePath string) (*model.ArchiveManifest, error) {
	resources, err := ExtractArchiveResources(htmlContent, book.URL, archiveExcludedDomains(deps))
	if err != nil {
		return nil, err
	}

	archive, err := warc.Open(archivePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %v", err)
	}
	defer archive.Close()

	for i, res := range resources {
		resources[i].Status, resources[i].Error = verifyArchivedResource(archive, res)
	}

	return &model.ArchiveManifest{
		BookmarkID: book.ID,
		URL:        book.URL,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
		Resources:  resources,
	}, nil
}

// verifyArchivedResource checks that a resource is properly stored in the
// archive. The archiver stores error responses (e.g. a 404 page) as if they
// were the resource itself, so an existing entry whose content is empty or
// looks like an error response is also considered missing.
func verifyArchivedResource(archive *warc.Archive, res model.ArchiveResource) (model.ArchiveResourceStatus, string) {
	// Scripts are stripped from archived pages by design, so they are never
	// stored in the archive
	if res.Type == model.ArchiveResourceScript {
		return model.ArchiveResourceSkipped, "scripts are not stored in archives"
	}

	if !archive.HasResource(res.Name) {
		return model.ArchiveResourceFailed, "resource not found in archive"
	}

	content, contentType, err := archive.Read(res.Name)
	if err != nil {
		return model.ArchiveResourceFailed, fmt.Sprintf("resource not readable: %v", err)
	}

	// Content is stored gzipped: decompress it to check it is not empty
	gzReader, err := gzip.NewReader(bytes.NewReader(content))
	if err != nil {
		return model.ArchiveResourceFailed, "resource content is not valid"
	}
	decompressed, err := io.ReadAll(gzReader)
	gzReader.Close()
	if err != nil {
		return model.ArchiveResourceFailed, "resource content is not valid"
	}

	if len(decompressed) == 0 {
		return model.ArchiveResourceFailed, "resource content is empty"
	}

	// An image or stylesheet is never legitimately served as an HTML document,
	// plain text or JSON, so this means an error response was archived instead
	lowerContentType := strings.ToLower(contentType)
	if strings.Contains(lowerContentType, "text/html") ||
		strings.Contains(lowerContentType, "text/plain") ||
		strings.Contains(lowerContentType, "application/json") {
		return model.ArchiveResourceFailed, fmt.Sprintf("archived content has unexpected type %q", contentType)
	}

	return model.ArchiveResourceOK, ""
}

// SaveArchiveManifest writes the archive manifest of a bookmark to storage.
func SaveArchiveManifest(deps model.Dependencies, manifest *model.ArchiveManifest) error {
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode archive manifest: %v", err)
	}

	book := &model.BookmarkDTO{Bookmark: model.Bookmark{ID: manifest.BookmarkID}}
	return deps.Domains().Storage().WriteData(model.GetArchiveManifestPath(book), data)
}

// LoadArchiveManifest reads the archive manifest of a bookmark from storage.
func LoadArchiveManifest(deps model.Dependencies, book *model.BookmarkDTO) (*model.ArchiveManifest, error) {
	data, err := afero.ReadFile(deps.Domains().Storage().FS(), model.GetArchiveManifestPath(book))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrArchiveManifestNotFound
		}
		return nil, fmt.Errorf("failed to read archive manifest: %v", err)
	}

	var manifest model.ArchiveManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse archive manifest: %v", err)
	}

	return &manifest, nil
}

// RetryArchiveResources downloads the missing resources of a bookmark archive
// (optionally limited to the submitted URLs), stores them in the archive and
// updates the manifest accordingly.
func RetryArchiveResources(deps model.Dependencies, book *model.BookmarkDTO, urls []string) (*model.ArchiveManifest, error) {
	manifest, err := LoadArchiveManifest(deps, book)
	if err != nil {
		return nil, err
	}

	missing := manifest.MissingResources(urls...)
	if len(missing) == 0 {
		return manifest, nil
	}

	// FIXME: This only works in local filesystem
	archivePath := filepath.Join(deps.Config().Storage.DataDir, model.GetArchivePath(book))
	if _, err := os.Stat(archivePath); err != nil {
		return nil, fmt.Errorf("archive for bookmark %d doesn't exist", book.ID)
	}

	// Download missing resources concurrently
	type downloadResult struct {
		content     []byte
		contentType string
		err         error
	}
	results := make(map[string]*downloadResult, len(missing))

	var mx sync.Mutex
	var wg sync.WaitGroup
	semaphore := make(chan struct{}, 5)

	for _, res := range missing {
		wg.Add(1)
		go func(res model.ArchiveResource) {
			defer wg.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			result := &downloadResult{}
			content, contentType, err := downloadArchiveResource(res.URL)
			if err != nil {
				result.err = err
			} else {
				result.content = content
				result.contentType = contentType
			}

			mx.Lock()
			results[res.URL] = result
			mx.Unlock()
		}(res)
	}
	wg.Wait()

	// Store downloaded resources in the archive
	db, err := bbolt.Open(archivePath, 0o600, &bbolt.Options{Timeout: 10 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("failed to open archive: %v", err)
	}
	defer db.Close()

	err = db.Update(func(tx *bbolt.Tx) error {
		for _, res := range missing {
			result := results[res.URL]
			if result.err != nil {
				continue
			}

			buffer := bytes.NewBuffer(nil)
			gzipper := gzip.NewWriter(buffer)
			if _, err := gzipper.Write(result.content); err != nil {
				return fmt.Errorf("failed to compress %s: %v", res.URL, err)
			}
			if err := gzipper.Close(); err != nil {
				return fmt.Errorf("failed to compress %s: %v", res.URL, err)
			}

			bucket, err := tx.CreateBucketIfNotExists([]byte(res.Name))
			if err != nil {
				return fmt.Errorf("failed to store %s: %v", res.URL, err)
			}
			if err := bucket.Put([]byte("content"), buffer.Bytes()); err != nil {
				return fmt.Errorf("failed to store %s: %v", res.URL, err)
			}
			if err := bucket.Put([]byte("type"), []byte(result.contentType)); err != nil {
				return fmt.Errorf("failed to store %s: %v", res.URL, err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Update the manifest with the retry results
	for i, res := range manifest.Resources {
		result, ok := results[res.URL]
		if !ok {
			continue
		}
		if result.err != nil {
			manifest.Resources[i].Status = model.ArchiveResourceFailed
			manifest.Resources[i].Error = result.err.Error()
		} else {
			manifest.Resources[i].Status = model.ArchiveResourceOK
			manifest.Resources[i].Error = ""
		}
	}
	manifest.CreatedAt = time.Now().UTC().Format(time.RFC3339)

	if err := SaveArchiveManifest(deps, manifest); err != nil {
		return nil, err
	}

	return manifest, nil
}

// downloadArchiveResource fetches a single resource and returns its content
// and content type.
func downloadArchiveResource(url string) ([]byte, string, error) {
	req, err := nurl.ParseRequestURI(url)
	if err != nil || req.Scheme == "" || req.Hostname() == "" {
		return nil, "", fmt.Errorf("url %q is not valid", url)
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	content, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read response: %v", err)
	}

	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return content, contentType, nil
}
