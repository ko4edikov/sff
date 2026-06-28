package source

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// archiveContentTypes are static-resource content types whose .resource is a zip
// that sf expands into a directory. Mirrors SDR's ARCHIVE_MIME_TYPES.
var archiveContentTypes = map[string]bool{
	"application/zip":              true,
	"application/x-zip-compressed": true,
	"application/jar":              true,
}

// staticResourceExt maps a content type to the extension sf gives the expanded
// .resource, reproducing SDR's getExtensionFromType (mime.getExtension, then its
// FALLBACK_TYPE_MAP). Anything not listed falls back to "bin"
// (application/octet-stream), matching SDR's final default.
var staticResourceExt = map[string]string{
	"application/javascript":   "js",
	"application/json":         "json",
	"text/css":                 "css",
	"text/csv":                 "csv",
	"text/plain":               "txt",
	"text/html":                "html",
	"text/xml":                 "xml",
	"application/xml":          "xml",
	"image/png":                "png",
	"image/jpeg":               "jpeg",
	"image/gif":                "gif",
	"image/x-icon":             "ico",
	"image/vnd.microsoft.icon": "ico",
	"image/svg+xml":            "svg",
	"image/webp":               "webp",
	"image/bmp":                "bmp",
	"image/tiff":               "tif",
	"application/pdf":          "pdf",
	"application/octet-stream": "bin",
	"application/vnd.ms-excel": "xls",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet": "xlsx",
	"application/vnd.ms-powerpoint":                                     "ppt",
	"application/msword":                                                "doc",
	"font/woff":                                                         "woff",
	"font/woff2":                                                        "woff2",
	"font/ttf":                                                          "ttf",
	"application/vnd.ms-fontobject":                                     "eot",
	"audio/mpeg":                                                        "mpga",
	"video/mp4":                                                         "mp4",
	// SDR FALLBACK_TYPE_MAP entries (mime returns no extension for these):
	"text/javascript":          "js",
	"application/x-javascript": "js",
	"text/x-haml":              "haml",
	"image/x-png":              "png",
}

// staticResourceContentTypes pre-scans a retrieve zip and maps each static
// resource's base path (e.g. "staticresources/Foo") to its content type, read
// from the sibling .resource-meta.xml. The binary entry needs this to choose its
// extension regardless of zip entry order.
func staticResourceContentTypes(zr *zip.Reader) map[string]string {
	out := map[string]string{}
	for _, f := range zr.File {
		name := filepath.ToSlash(f.Name)
		if !strings.HasPrefix(name, "staticresources/") || !strings.HasSuffix(name, ".resource-meta.xml") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			continue
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(name, ".resource-meta.xml")
		out[base] = staticContentType(data)
	}
	return out
}

var contentTypeRe = regexp.MustCompile(`<contentType>([^<]*)</contentType>`)

// staticContentType reads the <contentType> from a .resource-meta.xml, defaulting
// to application/octet-stream when absent (as SDR does).
func staticContentType(metaXML []byte) string {
	if m := contentTypeRe.FindSubmatch(metaXML); m != nil {
		if ct := strings.TrimSpace(string(m[1])); ct != "" {
			return ct
		}
	}
	return "application/octet-stream"
}

// staticExt returns the source file extension for a static resource content type.
func staticExt(contentType string) string {
	if e, ok := staticResourceExt[contentType]; ok {
		return e
	}
	return "bin"
}

// convertStaticResource turns a metadata-format .resource binary into its
// source-format file(s): a single file renamed to the content type's extension,
// or — for archive content types — the unzipped tree under a directory named
// after the resource. binaryRel is e.g. "staticresources/Foo.resource".
func convertStaticResource(binaryRel string, binary []byte, contentType string) ([]splitFile, error) {
	base := strings.TrimSuffix(binaryRel, ".resource") // staticresources/Foo
	if archiveContentTypes[contentType] {
		return unzipStaticResource(base, binary)
	}
	return []splitFile{{rel: base + "." + staticExt(contentType), data: binary}}, nil
}

// unzipStaticResource expands an archive .resource into files under base/.
func unzipStaticResource(base string, data []byte) ([]splitFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open static resource archive: %w", err)
	}
	var out []splitFile
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("read archive entry %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read archive entry %s: %w", f.Name, err)
		}
		out = append(out, splitFile{rel: path.Join(base, filepath.ToSlash(f.Name)), data: b})
	}
	return out, nil
}
