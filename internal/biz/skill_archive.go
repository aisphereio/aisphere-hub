package biz

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/aisphereio/kernel/errorx"
	"go.yaml.in/yaml/v3"
)

const (
	defaultMaxArchiveBytes  = 50 << 20
	defaultMaxUnpackedBytes = 200 << 20
	defaultMaxArchiveFiles  = 2000
	defaultMaxArchiveFile   = 50 << 20
)

type SkillArchiveLimits struct {
	MaxArchiveBytes  int64
	MaxUnpackedBytes int64
	MaxFiles         int
	MaxFileBytes     int64
}

type skillArchiveFrontMatter struct {
	Name        string `yaml:"name"`
	DisplayName string `yaml:"display_name"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
}

func ParseSkillArchive(data []byte, limits SkillArchiveLimits) (*SkillArchive, error) {
	limits = normalizeSkillArchiveLimits(limits)
	if len(data) == 0 {
		return nil, archiveError(ErrSkillArchiveInvalid, "zip content is required")
	}
	if int64(len(data)) > limits.MaxArchiveBytes {
		return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("zip is %d bytes, max is %d", len(data), limits.MaxArchiveBytes))
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, archiveError(ErrSkillArchiveInvalid, "zip content is not readable")
	}
	files := make([]SkillArchiveFile, 0, len(reader.File))
	seen := map[string]struct{}{}
	var skillMD string
	var total int64
	for _, entry := range reader.File {
		info := entry.FileInfo()
		if info.IsDir() {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, archiveError(ErrSkillArchiveInvalid, "symlinks are not allowed: "+entry.Name)
		}
		p, err := normalizeArchiveFilePath(entry.Name)
		if err != nil {
			return nil, archiveError(ErrSkillArchiveInvalid, err.Error())
		}
		if _, ok := seen[p]; ok {
			return nil, archiveError(ErrSkillArchiveInvalid, "duplicate file path: "+p)
		}
		seen[p] = struct{}{}
		if len(files)+1 > limits.MaxFiles {
			return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("file count exceeds %d", limits.MaxFiles))
		}
		size := int64(entry.UncompressedSize64)
		if size > limits.MaxFileBytes {
			return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("file %s is %d bytes, max is %d", p, size, limits.MaxFileBytes))
		}
		if total+size > limits.MaxUnpackedBytes {
			return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("unpacked size exceeds %d bytes", limits.MaxUnpackedBytes))
		}
		content, err := readZipFile(entry, limits.MaxFileBytes)
		if err != nil {
			return nil, err
		}
		total += int64(len(content))
		if total > limits.MaxUnpackedBytes {
			return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("unpacked size exceeds %d bytes", limits.MaxUnpackedBytes))
		}
		if p == "SKILL.md" {
			skillMD = string(content)
		}
		files = append(files, SkillArchiveFile{Path: p, Content: content})
	}
	if strings.TrimSpace(skillMD) == "" {
		return nil, archiveError(ErrSkillArchiveMissingMeta, "root SKILL.md is required")
	}
	name, displayName, description, err := ParseSkillMetadataDocument(skillMD)
	if err != nil {
		return nil, archiveError(ErrSkillArchiveInvalid, err.Error())
	}
	if strings.TrimSpace(description) == "" {
		return nil, archiveError(ErrSkillArchiveInvalid, "SKILL.md description is required")
	}
	return &SkillArchive{
		Name:          name,
		DisplayName:   displayName,
		Description:   description,
		Files:         files,
		FileCount:     len(files),
		UnpackedBytes: total,
	}, nil
}

func ParseSkillMetadataDocument(content string) (name, displayName, description string, err error) {
	content = strings.TrimPrefix(content, "\ufeff")
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", "", fmt.Errorf("SKILL.md must start with YAML front-matter")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", "", fmt.Errorf("SKILL.md front-matter is not closed")
	}
	var fm skillArchiveFrontMatter
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &fm); err != nil {
		return "", "", "", fmt.Errorf("parse SKILL.md front-matter: %w", err)
	}
	name = strings.TrimSpace(fm.Name)
	if name == "" {
		return "", "", "", fmt.Errorf("SKILL.md name is required")
	}
	displayName = strings.TrimSpace(fm.DisplayName)
	if displayName == "" {
		displayName = strings.TrimSpace(fm.Title)
	}
	if displayName == "" {
		for _, line := range lines[end+1:] {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				displayName = strings.TrimSpace(strings.TrimPrefix(line, "# "))
				break
			}
		}
	}
	if displayName == "" {
		displayName = name
	}
	return name, displayName, strings.TrimSpace(fm.Description), nil
}

func normalizeSkillArchiveLimits(limits SkillArchiveLimits) SkillArchiveLimits {
	if limits.MaxArchiveBytes <= 0 {
		limits.MaxArchiveBytes = defaultMaxArchiveBytes
	}
	if limits.MaxUnpackedBytes <= 0 {
		limits.MaxUnpackedBytes = defaultMaxUnpackedBytes
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaultMaxArchiveFiles
	}
	if limits.MaxFileBytes <= 0 {
		limits.MaxFileBytes = defaultMaxArchiveFile
	}
	return limits
}

func normalizeArchiveFilePath(raw string) (string, error) {
	raw = strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	if raw == "" || strings.HasPrefix(raw, "/") || strings.Contains(raw, ":") {
		return "", fmt.Errorf("invalid archive path: %s", raw)
	}
	clean := path.Clean(raw)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid archive path: %s", raw)
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") || strings.Contains(clean, "/.git/") {
		return "", fmt.Errorf(".git paths are not allowed: %s", clean)
	}
	return clean, nil
}

func readZipFile(entry *zip.File, maxBytes int64) ([]byte, error) {
	rc, err := entry.Open()
	if err != nil {
		return nil, archiveError(ErrSkillArchiveInvalid, "read file failed: "+entry.Name)
	}
	defer rc.Close()
	limited := &io.LimitedReader{R: rc, N: maxBytes + 1}
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, archiveError(ErrSkillArchiveInvalid, "read file failed: "+entry.Name)
	}
	if int64(len(content)) > maxBytes {
		return nil, archiveError(ErrSkillArchiveTooLarge, fmt.Sprintf("file %s exceeds %d bytes", entry.Name, maxBytes))
	}
	return content, nil
}

func archiveError(base error, message string) error {
	return errorx.From(base, errorx.WithMessage(message))
}
