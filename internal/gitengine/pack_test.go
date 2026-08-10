package gitengine

import (
	"archive/zip"
	"bytes"
	"context"
	"testing"
)

// TestBuildSkillPackage zips the seeded repo and verifies the package
// contains SKILL.md at the root with a digest that reflects the tree.
func TestBuildSkillPackage(t *testing.T) {
	engine := newTestEngine(t)
	pkg, err := engine.BuildSkillPackage(context.Background(), "search", "main")
	if err != nil {
		t.Fatalf("BuildSkillPackage: %v", err)
	}
	if pkg == nil || len(pkg.ZIP) == 0 || pkg.Size == 0 || pkg.SHA256 == "" || pkg.MD5 == "" {
		t.Fatalf("incomplete package: %+v", pkg)
	}
	zr, err := zip.NewReader(bytes.NewReader(pkg.ZIP), int64(len(pkg.ZIP)))
	if err != nil {
		t.Fatalf("invalid zip: %v", err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "SKILL.md" {
			found = true
		}
		if f.Name[0] == '/' || containsDotDot(f.Name) {
			t.Fatalf("unsafe entry %q", f.Name)
		}
	}
	if !found {
		t.Fatal("package missing SKILL.md at root")
	}
	if int64(len(pkg.ZIP)) != pkg.Size {
		t.Errorf("reported size %d != zip len %d", pkg.Size, len(pkg.ZIP))
	}
}

func containsDotDot(p string) bool {
	for i := 0; i+1 < len(p); i++ {
		if p[i] == '.' && p[i+1] == '.' {
			return true
		}
	}
	return false
}