package gitengine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// BuildSkillPackage zips the whole working tree at `ref` (any valid git ref:
// "main", or "v1.0.0" / "refs/tags/v1.0.0") with deterministic file entries.
// SKILL.md ends up at the package root, which is what the runtime unzips.
func (e *Engine) BuildSkillPackage(ctx context.Context, name, ref string) (*biz.SkillPackageData, error) {
	softRepo, err := e.open(ctx, name)
	if err != nil {
		return nil, errGit(err)
	}
	if ref == "" {
		ref = "HEAD"
	}
	hashStr, err := softRepo.ShowRefVerify(normalizeRef(ref))
	if err != nil {
		return nil, fmt.Errorf("gitengine: resolve %s: %w", ref, err)
	}
	repo, err := gogit.PlainOpen(softRepo.Path)
	if err != nil {
		return nil, errGit(err)
	}
	commit, err := repo.CommitObject(plumbing.NewHash(hashStr))
	if err != nil {
		return nil, errGit(err)
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, errGit(err)
	}
	// List files (ignoring gitlinks/submodules) deterministically.
	type entry struct {
		name string
	}
	var entries []entry
	if err := tree.Files().ForEach(func(f *object.File) error {
		if f.Mode == filemode.Submodule {
			return nil
		}
		entries = append(entries, entry{f.Name})
		return nil
	}); err != nil {
		return nil, errGit(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, ent := range entries {
		name := path.Clean(ent.name)
		if name == "." || strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			continue
		}
		f, err := tree.File(ent.name)
		if err != nil {
			return nil, errGit(err)
		}
		header := &zip.FileHeader{Name: ent.name, Method: zip.Deflate}
		fm, err := f.Mode.ToOSFileMode()
		if err != nil {
			return nil, errGit(err)
		}
		header.SetMode(fm)
		header.SetModTime(commit.Committer.When)
		w, err := zw.CreateHeader(header)
		if err != nil {
			return nil, errGit(err)
		}
		rc, err := f.Reader()
		if err != nil {
			return nil, errGit(err)
		}
		_, err = io.Copy(w, rc)
		_ = rc.Close()
		if err != nil {
			return nil, errGit(err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, errGit(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	dest := md5.Sum(buf.Bytes())
	return &biz.SkillPackageData{
		SHA256: fmt.Sprintf("%x", sum),
		MD5:    fmt.Sprintf("%x", dest),
		Size:   int64(buf.Len()),
		ZIP:    buf.Bytes(),
	}, nil
}
