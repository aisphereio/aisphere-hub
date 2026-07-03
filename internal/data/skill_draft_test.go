package data

import (
	"testing"

	"github.com/aisphereio/kernel/errorx"
)

func TestRejectNonRecursiveNonEmptyDraftDirectoryDelete(t *testing.T) {
	target := &skillDraftFileModel{Path: "src", Kind: skillDraftKindDirectory}

	if err := rejectNonRecursiveNonEmptyDraftDirectoryDelete(target, false, 2); err == nil {
		t.Fatal("expected conflict for non-recursive delete of non-empty directory")
	} else if errorx.CodeOf(err) != errorx.Code("SKILL_DRAFT_DIRECTORY_NOT_EMPTY") {
		t.Fatalf("code = %s, want SKILL_DRAFT_DIRECTORY_NOT_EMPTY", errorx.CodeOf(err))
	} else if errorx.HTTPStatusOf(err) != 409 {
		t.Fatalf("http status = %d, want 409", errorx.HTTPStatusOf(err))
	}

	if err := rejectNonRecursiveNonEmptyDraftDirectoryDelete(target, true, 2); err != nil {
		t.Fatalf("recursive delete should be allowed: %v", err)
	}
	if err := rejectNonRecursiveNonEmptyDraftDirectoryDelete(target, false, 0); err != nil {
		t.Fatalf("empty directory delete should be allowed: %v", err)
	}
	if err := rejectNonRecursiveNonEmptyDraftDirectoryDelete(&skillDraftFileModel{Path: "src/main.go", Kind: skillDraftKindFile}, false, 1); err != nil {
		t.Fatalf("file delete should be allowed regardless of child count: %v", err)
	}
}
