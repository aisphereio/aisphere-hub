package biz

import (
	"archive/zip"
	"bytes"
	"errors"
	"testing"
)

func TestParseSkillArchiveRequiresSkillMD(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"README.md": "# Missing metadata\n",
	})
	_, err := ParseSkillArchive(data, SkillArchiveLimits{})
	if !errors.Is(err, ErrSkillArchiveMissingMeta) {
		t.Fatalf("ParseSkillArchive() error = %v, want ErrSkillArchiveMissingMeta", err)
	}
}

func TestParseSkillArchiveAcceptsSingleTopLevelDirectory(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"search-skill/SKILL.md":    "---\nname: search\ndisplay_name: Search Skill\ndescription: Search tools\n---\n# Search\n",
		"search-skill/src/main.py": "print('ok')\n",
		"__MACOSX/._SKILL.md":      "ignored",
	})
	archive, err := ParseSkillArchive(data, SkillArchiveLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if archive.Name != "search" || archive.DisplayName != "Search Skill" || archive.Description != "Search tools" {
		t.Fatalf("metadata = %+v", archive)
	}
	if archive.FileCount != 2 || len(archive.Files) != 2 {
		t.Fatalf("file count = %d/%d, want 2", archive.FileCount, len(archive.Files))
	}
	if archive.Files[0].Path != "SKILL.md" && archive.Files[1].Path != "SKILL.md" {
		t.Fatalf("normalized files = %+v, want root SKILL.md", archive.Files)
	}
	for _, file := range archive.Files {
		if file.Path == "search-skill/src/main.py" {
			t.Fatalf("wrapper directory was not stripped: %+v", archive.Files)
		}
	}
}

func TestParseSkillArchiveRejectsMixedWrapperLayout(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"search-skill/SKILL.md": "---\nname: search\ndescription: Search tools\n---\n",
		"README.md":             "outside wrapper",
	})
	_, err := ParseSkillArchive(data, SkillArchiveLimits{})
	if !errors.Is(err, ErrSkillArchiveInvalid) {
		t.Fatalf("ParseSkillArchive() error = %v, want ErrSkillArchiveInvalid", err)
	}
}

func TestParseSkillArchiveRequiresNameAndDescription(t *testing.T) {
	tests := []struct {
		name    string
		skillMD string
	}{
		{name: "missing name", skillMD: "---\ndescription: desc\n---\n"},
		{name: "missing description", skillMD: "---\nname: search\n---\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := zipBytes(t, map[string]string{"SKILL.md": tt.skillMD})
			_, err := ParseSkillArchive(data, SkillArchiveLimits{})
			if !errors.Is(err, ErrSkillArchiveInvalid) {
				t.Fatalf("ParseSkillArchive() error = %v, want ErrSkillArchiveInvalid", err)
			}
		})
	}
}

func TestParseSkillArchiveRejectsInvalidPath(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"SKILL.md":  "---\nname: search\ndescription: Search tools\n---\n",
		"../escape": "bad",
	})
	_, err := ParseSkillArchive(data, SkillArchiveLimits{})
	if !errors.Is(err, ErrSkillArchiveInvalid) {
		t.Fatalf("ParseSkillArchive() error = %v, want ErrSkillArchiveInvalid", err)
	}
}

func TestParseSkillArchiveSuccess(t *testing.T) {
	data := zipBytes(t, map[string]string{
		"SKILL.md":       "---\nname: search\ndisplay_name: Search Skill\ndescription: Search tools\n---\n# Search\n",
		"skill.yaml":     "entry: main.py\n",
		"src/main.py":    "print('ok')\n",
		"docs/README.md": "# Docs\n",
	})
	archive, err := ParseSkillArchive(data, SkillArchiveLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if archive.Name != "search" || archive.DisplayName != "Search Skill" || archive.Description != "Search tools" {
		t.Fatalf("metadata = %+v", archive)
	}
	if archive.FileCount != 4 || len(archive.Files) != 4 {
		t.Fatalf("file count = %d/%d, want 4", archive.FileCount, len(archive.Files))
	}
}

func zipBytes(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
