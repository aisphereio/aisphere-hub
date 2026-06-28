package skillzip

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	SkillMDFile           = "SKILL.md"
	DefaultInitialVersion = "0.0.1"

	MaxUploadBytes       = 20 << 20
	MaxEntries           = 500
	MaxUncompressedBytes = 50 << 20
	EncodingKey          = "encoding"
	EncodingBase64       = "base64"
)

type Skill struct {
	Name        string
	Description string
	Version     string
	SkillMD     string
	Metadata    map[string]string
	Resources   []Resource
}

type Resource struct {
	Path    string
	Name    string
	Type    string
	Content string
	Size    int64
	Binary  bool
}

type zipEntry struct {
	path string
	file *zip.File
}

func ParseSkillFromZip(zipBytes []byte) (*Skill, error) {
	if len(zipBytes) == 0 {
		return nil, fmt.Errorf("empty skill package")
	}
	if len(zipBytes) > MaxUploadBytes {
		return nil, fmt.Errorf("skill package exceeds %d bytes", MaxUploadBytes)
	}

	reader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return nil, fmt.Errorf("open skill zip: %w", err)
	}
	if len(reader.File) > MaxEntries {
		return nil, fmt.Errorf("skill package has too many entries")
	}

	entries := make([]zipEntry, 0, len(reader.File))
	for _, f := range reader.File {
		if f.FileInfo().IsDir() {
			continue
		}
		cleanPath, ok := normalizeZipPath(f.Name)
		if !ok {
			return nil, fmt.Errorf("invalid zip path %q", f.Name)
		}
		entries = append(entries, zipEntry{path: cleanPath, file: f})
	}
	root, err := detectRoot(entries)
	if err != nil {
		return nil, err
	}

	var skillMD string
	resources := make([]Resource, 0, len(reader.File))
	var total int64
	for _, entry := range entries {
		relPath := stripDetectedRoot(entry.path, root)
		if relPath == "" {
			continue
		}
		content, err := readZipFile(entry.file)
		if err != nil {
			return nil, err
		}
		total += int64(len(content))
		if total > MaxUncompressedBytes {
			return nil, fmt.Errorf("skill package uncompressed content exceeds %d bytes", MaxUncompressedBytes)
		}
		if relPath == SkillMDFile {
			skillMD = string(content)
			continue
		}
		resources = append(resources, newResource(relPath, content))
	}
	if strings.TrimSpace(skillMD) == "" {
		return nil, fmt.Errorf("skill package missing %s", SkillMDFile)
	}

	meta, body := ParseFrontMatter(skillMD)
	name := strings.TrimSpace(meta["name"])
	if name == "" {
		return nil, fmt.Errorf("skill metadata missing name")
	}
	description := strings.TrimSpace(meta["description"])
	if description == "" {
		return nil, fmt.Errorf("skill metadata missing description")
	}
	version := strings.TrimSpace(meta["version"])
	if version == "" {
		version = DefaultInitialVersion
	}
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("skill markdown body is required")
	}
	sort.Slice(resources, func(i, j int) bool { return resources[i].Path < resources[j].Path })

	return &Skill{
		Name:        name,
		Description: description,
		Version:     version,
		SkillMD:     skillMD,
		Metadata:    meta,
		Resources:   resources,
	}, nil
}

func ParseFrontMatter(content string) (map[string]string, string) {
	meta := map[string]string{}
	s := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(s, "---\n") {
		return meta, content
	}
	rest := strings.TrimPrefix(s, "---\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return meta, content
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key != "" {
			meta[key] = value
		}
	}
	body := strings.TrimPrefix(rest[end:], "\n---")
	body = strings.TrimPrefix(body, "---")
	body = strings.TrimPrefix(body, "\n")
	return meta, body
}

func normalizeZipPath(name string) (string, bool) {
	name = strings.ReplaceAll(name, "\\", "/")
	name = strings.TrimPrefix(name, "/")
	clean := path.Clean(name)
	if clean == "." || clean == "" || strings.HasPrefix(clean, "../") || clean == ".." || strings.Contains(clean, "/../") {
		return "", false
	}
	return clean, true
}

func detectRoot(entries []zipEntry) (string, error) {
	for _, entry := range entries {
		if entry.path == SkillMDFile {
			return "", nil
		}
	}
	roots := map[string]struct{}{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.path, "/"+SkillMDFile) {
			root := strings.TrimSuffix(entry.path, "/"+SkillMDFile)
			if root != "" && !strings.Contains(root, "/") {
				roots[root] = struct{}{}
			}
		}
	}
	if len(roots) == 0 {
		return "", nil
	}
	if len(roots) > 1 {
		return "", fmt.Errorf("skill package contains multiple roots")
	}
	for root := range roots {
		return root, nil
	}
	return "", nil
}

func stripDetectedRoot(clean, root string) string {
	if root == "" {
		return clean
	}
	if clean == root {
		return ""
	}
	prefix := root + "/"
	if strings.HasPrefix(clean, prefix) {
		return strings.TrimPrefix(clean, prefix)
	}
	return ""
}

func readZipFile(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open zip file %s: %w", f.Name, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxUncompressedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read zip file %s: %w", f.Name, err)
	}
	return data, nil
}

func newResource(relPath string, content []byte) Resource {
	typ := strings.Split(relPath, "/")[0]
	if typ == relPath {
		typ = ""
	}
	binary := !isText(content)
	out := Resource{
		Path:   relPath,
		Name:   filepath.Base(relPath),
		Type:   typ,
		Size:   int64(len(content)),
		Binary: binary,
	}
	if binary {
		out.Content = base64.StdEncoding.EncodeToString(content)
		return out
	}
	out.Content = string(content)
	return out
}

func isText(b []byte) bool {
	for _, c := range b {
		if c == 0 {
			return false
		}
	}
	return true
}
