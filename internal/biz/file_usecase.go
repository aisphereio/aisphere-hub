package biz

import (
	"context"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
)

// FileUsecase is the biz orchestration layer for the file-content API.
// It mirrors the GitLab/Gitea repository-files REST shape but stays
// git-native underneath: every read/write lands in the embedded Soft
// Serve bare repo. Authz is enforced HERE (not in gitengine) because
// writes go through go-git PlainOpen and therefore bypass the
// receive-pack update hook that normally guards `git push`. The biz
// layer is the sole enforcement point for file CRUD.
type FileUsecase struct {
	git   SkillGitEngine
	authz *AuthzUsecase
}

func NewFileUsecase(git SkillGitEngine, authz *AuthzUsecase) *FileUsecase {
	return &FileUsecase{git: git, authz: authz}
}

// ListFiles lists entries at path on ref (default HEAD).
func (uc *FileUsecase) ListFiles(ctx context.Context, principal authn.Principal, name, path, ref string) ([]*FileInfo, error) {
	if err := uc.requireFilePermission(ctx, principal, name, ref, "view"); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}
	return uc.git.ListFiles(ctx, strings.TrimSpace(name), strings.TrimSpace(path), ref)
}

// GetFileContent fetches a single file's content + commit metadata.
func (uc *FileUsecase) GetFileContent(ctx context.Context, principal authn.Principal, name, path, ref string) (*FileContent, error) {
	if err := uc.requireFilePermission(ctx, principal, name, ref, "view"); err != nil {
		return nil, err
	}
	if ref == "" {
		ref = "HEAD"
	}
	return uc.git.GetFileContent(ctx, strings.TrimSpace(name), strings.TrimSpace(path), ref)
}

// CreateFile creates a new file. Refusing to clobber existing entries
// is enforced inside gitengine (returns ErrFileAlreadyExists).
func (uc *FileUsecase) FileCreate(ctx context.Context, principal authn.Principal, name, path, content, message, branch string) (*FileContent, error) {
	if err := uc.requireFilePermission(ctx, principal, name, branchRef(branch), "edit"); err != nil {
		return nil, err
	}
	committerName, committerEmail := resolveCommitter(principal)
	if message == "" {
		message = "Create " + path
	}
	return uc.git.CreateFile(ctx, strings.TrimSpace(name), strings.TrimSpace(path), content, message, defaultBranch(branch), committerName, committerEmail)
}

// UpdateFile updates an existing file. sha is the blob hash the client
// last saw; gitengine returns 409 (ErrFileAlreadyExists) when the blob
// has moved under it — the UI must refetch and let the user decide.
func (uc *FileUsecase) FileUpdate(ctx context.Context, principal authn.Principal, name, path, content, message, sha, branch string) (*FileContent, error) {
	if err := uc.requireFilePermission(ctx, principal, name, branchRef(branch), "edit"); err != nil {
		return nil, err
	}
	committerName, committerEmail := resolveCommitter(principal)
	if message == "" {
		message = "Update " + path
	}
	return uc.git.UpdateFile(ctx, strings.TrimSpace(name), strings.TrimSpace(path), content, message, sha, defaultBranch(branch), committerName, committerEmail)
}

// DeleteFile removes a file and returns the new commit identity.
func (uc *FileUsecase) FileDelete(ctx context.Context, principal authn.Principal, name, path, message, sha, branch string) (string, string, error) {
	if err := uc.requireFilePermission(ctx, principal, name, branchRef(branch), "edit"); err != nil {
		return "", "", err
	}
	committerName, committerEmail := resolveCommitter(principal)
	if message == "" {
		message = "Delete " + path
	}
	return uc.git.DeleteFile(ctx, strings.TrimSpace(name), strings.TrimSpace(path), message, sha, defaultBranch(branch), committerName, committerEmail)
}

// requireFilePermission maps the requested ref to the canonical skill
// permission and runs a fatal authz check. Default-branch writes need
// `publish` (matching RequiredPermissionForRefUpdate for `git push`);
// non-default writes and any read need only `edit`/`view`.
func (uc *FileUsecase) requireFilePermission(ctx context.Context, principal authn.Principal, name, ref, fallback string) error {
	if uc.authz == nil {
		return nil
	}
	permission := fallback
	if fallback == "edit" {
		permission = writePermissionFor(ref)
	}
	return uc.authz.Require(ctx, AuthzCheckRequest{
		Subject:    principalSubject(principal),
		Resource:   AuthzObjectRef{Type: "skill", ID: strings.TrimSpace(name)},
		Permission: permission,
		TenantID:   principal.TenantID,
		OrgID:      principal.OrgID,
	})
}

// writePermissionFor mirrors RequiredPermissionForRefUpdate so the
// file-content API cannot grant more than `git push` would. A write
// to the default branch is a publish; anything else is an edit.
func writePermissionFor(ref string) string {
	ref = strings.TrimSpace(ref)
	switch {
	case ref == "", ref == "HEAD", ref == "refs/heads/"+SkillDefaultBranch, ref == SkillDefaultBranch:
		return "publish"
	default:
		return "edit"
	}
}

// branchRef normalises the caller's branch into a refs/heads form so
// writePermissionFor can compare against the default branch cleanly.
func branchRef(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "refs/heads/" + SkillDefaultBranch
	}
	if strings.HasPrefix(branch, "refs/") {
		return branch
	}
	return "refs/heads/" + branch
}

func defaultBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return SkillDefaultBranch
	}
	return branch
}

// resolveCommitter picks a committer identity from the principal,
// falling back to the same identity seedInitialCommit uses so
// automated tooling without a human principal still produces tidy
// history.
func resolveCommitter(principal authn.Principal) (string, string) {
	name := strings.TrimSpace(principal.Username)
	email := strings.TrimSpace(principal.Email)
	if name != "" && email != "" {
		return name, email
	}
	if name == "" {
		name = "Aisphere Hub"
	}
	if email == "" {
		email = "noreply@aisphere.io"
	}
	return name, email
}

// now is a seam for tests; production uses time.Now.
var now = func() time.Time { return time.Now() }
