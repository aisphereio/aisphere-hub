// Package gitengine file.go implements the file-content API on top of the
// embedded Soft Serve bare repo. Reads go through the Soft Serve SDK
// (repo.LsTree/Tree.Entries/TreeEntry.Contents) so they reuse the same
// git plumbing ListReleases already uses. Writes go through go-git's
// PlainOpen on the same bare repo path — this is the same approach
// aisphere-git-server took, ported and with the critical buildTreeWithFile
// bug fixed (the original discarded every sibling entry on each write,
// so updating one file in a directory silently deleted the rest).
//
// Authz is NOT enforced here. File CRUD bypasses the receive-pack update
// hook (we write directly to the object store + ref), so biz.FileUsecase
// runs Require() before calling any of these. This layer is purely
// mechanical.
package gitengine

import (
	"context"
	"encoding/base64"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/errorx"
	softgit "github.com/aisphereio/soft-serve/git"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// --- error helpers ----------------------------------------------------------

// errGit wraps a lower-level go-git / soft-serve failure into the biz
// ErrGitOperationFailed kernel error so callers see a consistent 500.
func errGit(cause error) error {
	if cause == nil {
		return nil
	}
	return errorx.From(biz.ErrGitOperationFailed, errorx.WithMessage(cause.Error()))
}

func errFileNotFound(p string) error {
	return errorx.From(biz.ErrFileNotFound, errorx.WithMessage("file not found: "+p))
}

func errBranchNotFound(ref string) error {
	return errorx.From(biz.ErrBranchNotFound, errorx.WithMessage("ref not found: "+ref))
}

func errFileExists(p string) error {
	return errorx.From(biz.ErrFileAlreadyExists, errorx.WithMessage("file already exists: "+p))
}

func errPathInvalid(p string) error {
	return errorx.From(biz.ErrFilePathInvalid, errorx.WithMessage("invalid path: "+p))
}

// --- reads (Soft Serve SDK) -------------------------------------------------

// ListFiles lists the entries at path on ref. An empty path lists the
// repo root. Directories get Type="dir", files Type="file".
func (e *Engine) ListFiles(ctx context.Context, name, listPath, ref string) ([]*biz.FileInfo, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, errGit(err)
	}
	if ref == "" {
		ref = "HEAD"
	}
	tree, err := repo.LsTree(ref)
	if err != nil {
		// soft-serve returns a sentinel for missing refs; normalise it.
		if isNoSuchRef(err) {
			return nil, errBranchNotFound(ref)
		}
		return nil, errGit(err)
	}
	if listPath = strings.TrimSpace(listPath); listPath != "" && listPath != "." && listPath != "/" {
		sub, err := tree.SubTree(listPath)
		if err != nil {
			if isNoSuchPath(err) {
				return nil, errFileNotFound(listPath)
			}
			return nil, errGit(err)
		}
		tree = sub
	}
	entries, err := tree.Entries()
	if err != nil {
		return nil, errGit(err)
	}
	entries.Sort()
	out := make([]*biz.FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entryToFileInfo(entry, tree.Path, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, nil
}

// GetFileContent fetches a single file's content plus commit metadata.
// Content is base64-encoded so the proto field semantically matches its
// declaration (aisphere-git-server lied here — it returned plaintext
// while claiming base64 — we do not replicate that bug).
func (e *Engine) GetFileContent(ctx context.Context, name, filePath, ref string) (*biz.FileContent, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || filePath == "." || filePath == "/" {
		return nil, errPathInvalid(filePath)
	}
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, errGit(err)
	}
	if ref == "" {
		ref = "HEAD"
	}
	tree, err := repo.LsTree(ref)
	if err != nil {
		if isNoSuchRef(err) {
			return nil, errBranchNotFound(ref)
		}
		return nil, errGit(err)
	}
	entry, err := tree.TreeEntry(filePath)
	if err != nil {
		if isNoSuchPath(err) {
			return nil, errFileNotFound(filePath)
		}
		return nil, errGit(err)
	}
	if !entry.IsBlob() && !entry.IsExec() && !entry.IsSymlink() {
		return nil, errPathInvalid(filePath)
	}
	content, err := entry.Contents()
	if err != nil {
		return nil, errGit(err)
	}
	commitTime, commitSHA, commitMessage, err := e.lastCommitMeta(ctx, name, filePath, ref)
	if err != nil {
		// metadata is best-effort; never fail a read because of it
		commitTime = time.Time{}
	}
	return &biz.FileContent{
		Name:          path.Base(filePath),
		Path:          filePath,
		SHA:           entry.ID().String(),
		Size:          int64(len(content)),
		Content:       encodeBase64(content),
		Encoding:      "base64",
		Ref:           ref,
		CommitSHA:     commitSHA,
		CommitMessage: commitMessage,
		LastModified:  commitTime,
	}, nil
}

// --- writes (go-git PlainOpen) ---------------------------------------------

// CreateFile writes a new blob and commits it at path. Refuses to
// clobber an existing entry (returns ErrFileAlreadyExists).
func (e *Engine) CreateFile(ctx context.Context, name, filePath, content, message, branch, committerName, committerEmail string) (*biz.FileContent, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || strings.Contains(filePath, "..") || strings.HasPrefix(filePath, "/") {
		return nil, errPathInvalid(filePath)
	}
	r, headCommit, refName, err := e.openBare(ctx, name, branch)
	if err != nil {
		return nil, err
	}
	parentTree, err := parentTreeFor(r, headCommit)
	if err != nil {
		return nil, errGit(err)
	}
	if existing := findEntry(parentTree, filePath); existing != nil {
		return nil, errFileExists(filePath)
	}
	blobHash, size, err := writeBlob(r.Storer, []byte(content))
	if err != nil {
		return nil, errGit(err)
	}
	newTree, err := upsertFileInTree(r.Storer, parentTree, filePath, blobHash, filemode.Regular)
	if err != nil {
		return nil, errGit(err)
	}
	commitSHA, err := commitTreeFromParents(r.Storer, newTree, []plumbing.Hash{headCommit.Hash}, message, committerName, committerEmail)
	if err != nil {
		return nil, errGit(err)
	}
	if isSkillMetadataPath(filePath) && isDefaultBranchName(branch) {
		if _, _, err := ParseSkillMetadata(name, content); err != nil {
			return nil, errPathInvalid("SKILL.md: " + err.Error())
		}
	}
	if err := setBranchRef(r.Storer, refName, commitSHA, headCommit.Hash); err != nil {
		return nil, errGit(err)
	}
	if isSkillMetadataPath(filePath) && isDefaultBranchName(branch) {
		if err := e.SyncSkillMetadata(ctx, name, branch); err != nil {
			return nil, errGit(err)
		}
	}
	return &biz.FileContent{
		Name: path.Base(filePath), Path: filePath, SHA: blobHash.String(),
		Size: size, Content: encodeBase64([]byte(content)), Encoding: "base64",
		Ref: branch, CommitSHA: commitSHA.String(), CommitMessage: message, LastModified: time.Now(),
	}, nil
}

// UpdateFile replaces an existing blob. sha is the blob hash the client
// last saw; a mismatch means someone else landed a write first and we
// refuse with 409 (ErrFileAlreadyExists) so the UI can refetch.
func (e *Engine) UpdateFile(ctx context.Context, name, filePath, content, message, sha, branch, committerName, committerEmail string) (*biz.FileContent, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || strings.Contains(filePath, "..") || strings.HasPrefix(filePath, "/") {
		return nil, errPathInvalid(filePath)
	}
	r, headCommit, refName, err := e.openBare(ctx, name, branch)
	if err != nil {
		return nil, err
	}
	parentTree, err := parentTreeFor(r, headCommit)
	if err != nil {
		return nil, errGit(err)
	}
	existing := findEntry(parentTree, filePath)
	if existing == nil {
		return nil, errFileNotFound(filePath)
	}
	// Optimistic concurrency: caller-supplied sha (if any) must equal the
	// blob currently on disk. Empty sha means "no CAS" — allowed for the
	// first iteration of the editor before we wire the UI to send it.
	if sha != "" && existing.Hash.String() != sha {
		return nil, errFileExists(filePath)
	}
	blobHash, size, err := writeBlob(r.Storer, []byte(content))
	if err != nil {
		return nil, errGit(err)
	}
	newTree, err := upsertFileInTree(r.Storer, parentTree, filePath, blobHash, filemode.Regular)
	if err != nil {
		return nil, errGit(err)
	}
	commitSHA, err := commitTreeFromParents(r.Storer, newTree, []plumbing.Hash{headCommit.Hash}, message, committerName, committerEmail)
	if err != nil {
		return nil, errGit(err)
	}
	if isSkillMetadataPath(filePath) && isDefaultBranchName(branch) {
		if _, _, err := ParseSkillMetadata(name, content); err != nil {
			return nil, errPathInvalid("SKILL.md: " + err.Error())
		}
	}
	if err := setBranchRef(r.Storer, refName, commitSHA, headCommit.Hash); err != nil {
		return nil, errGit(err)
	}
	if isSkillMetadataPath(filePath) && isDefaultBranchName(branch) {
		if err := e.SyncSkillMetadata(ctx, name, branch); err != nil {
			return nil, errGit(err)
		}
	}
	return &biz.FileContent{
		Name: path.Base(filePath), Path: filePath, SHA: blobHash.String(),
		Size: size, Content: encodeBase64([]byte(content)), Encoding: "base64",
		Ref: branch, CommitSHA: commitSHA.String(), CommitMessage: message, LastModified: time.Now(),
	}, nil
}

// DeleteFile removes the entry at path and commits the result.
func (e *Engine) DeleteFile(ctx context.Context, name, filePath, message, sha, branch, committerName, committerEmail string) (string, string, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" || strings.Contains(filePath, "..") || strings.HasPrefix(filePath, "/") {
		return "", "", errPathInvalid(filePath)
	}
	if isSkillMetadataPath(filePath) && isDefaultBranchName(branch) {
		return "", "", errPathInvalid("SKILL.md cannot be deleted from the default branch")
	}
	r, headCommit, refName, err := e.openBare(ctx, name, branch)
	if err != nil {
		return "", "", err
	}
	parentTree, err := parentTreeFor(r, headCommit)
	if err != nil {
		return "", "", errGit(err)
	}
	existing := findEntry(parentTree, filePath)
	if existing == nil {
		return "", "", errFileNotFound(filePath)
	}
	if sha != "" && existing.Hash.String() != sha {
		return "", "", errFileExists(filePath)
	}
	newTree, err := removeFileFromTree(r.Storer, parentTree, filePath)
	if err != nil {
		return "", "", errGit(err)
	}
	commitSHA, err := commitTreeFromParents(r.Storer, newTree, []plumbing.Hash{headCommit.Hash}, message, committerName, committerEmail)
	if err != nil {
		return "", "", errGit(err)
	}
	if err := setBranchRef(r.Storer, refName, commitSHA, headCommit.Hash); err != nil {
		return "", "", errGit(err)
	}
	return commitSHA.String(), message, nil
}

// --- bare-repo open + ref resolution ---------------------------------------

// openBare opens the bare repo via go-git PlainOpen (same on-disk store
// Soft Serve already manages) and resolves branch to its head commit +
// canonical refs/heads/<branch> name. branch="" means the skill default.
func (e *Engine) openBare(ctx context.Context, name, branch string) (*gogit.Repository, *object.Commit, plumbing.ReferenceName, error) {
	softRepo, err := e.open(ctx, name)
	if err != nil {
		return nil, nil, "", errGit(err)
	}
	r, err := gogit.PlainOpen(softRepo.Path)
	if err != nil {
		return nil, nil, "", errGit(err)
	}
	if branch == "" {
		branch = biz.SkillDefaultBranch
	}
	refName := plumbing.NewBranchReferenceName(branch)
	ref, err := r.Reference(refName, true)
	if err != nil {
		if errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, nil, "", errBranchNotFound(branch)
		}
		return nil, nil, "", errGit(err)
	}
	commit, err := r.CommitObject(ref.Hash())
	if err != nil {
		return nil, nil, "", errGit(err)
	}
	return r, commit, refName, nil
}

// parentTreeFor returns the root tree of a commit, or an empty tree when
// the repo has no commits yet (initial commit path).
func parentTreeFor(r *gogit.Repository, head *object.Commit) (*object.Tree, error) {
	if head == nil {
		return emptyTree(r.Storer)
	}
	return head.Tree()
}

// lastCommitMeta walks the Soft Serve repo for the last commit touching
// filePath on ref, returning (when, sha, subject). Best-effort: callers
// ignore the error.
func (e *Engine) lastCommitMeta(ctx context.Context, name, filePath, ref string) (time.Time, string, string, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return time.Time{}, "", "", err
	}
	rref, err := repo.HEAD()
	if err != nil {
		return time.Time{}, "", "", err
	}
	_ = rref
	_ = filePath
	// Soft Serve SDK does not expose a per-path last-commit walker that is
	// cheap to call here; we surface commit metadata only on the write
	// return path (where we just made the commit) and leave reads with a
	// zero time. The UI still has SHA + size + content, which is what the
	// editor needs to render and save.
	return time.Time{}, "", "", nil
}

// --- tree mutation (the buildTreeWithFile fix lives here) ------------------

// upsertFileInTree loads the tree at root, walks down to the parent
// directory of filePath, and replaces-or-appends the leaf entry —
// PRESERVING every sibling entry. This is the rewrite of
// aisphere-git-server's buildTreeWithFile, which discarded siblings and
// silently deleted files whenever you updated one in a populated dir.
func upsertFileInTree(s storer.EncodedObjectStorer, root *object.Tree, filePath string, blobHash plumbing.Hash, mode filemode.FileMode) (plumbing.Hash, error) {
	parts := splitPath(filePath)
	return upsertEntry(s, root, parts, object.TreeEntry{Name: parts[len(parts)-1], Mode: mode, Hash: blobHash})
}

// upsertEntry walks down parts[0..n-1], materialising intermediate trees
// as needed, and writes the leaf entry into the deepest tree. Every
// ancestor tree is re-encoded and re-stored, so siblings are preserved
// at every level.
func upsertEntry(s storer.EncodedObjectStorer, tree *object.Tree, parts []string, leaf object.TreeEntry) (plumbing.Hash, error) {
	if len(parts) == 1 {
		newEntries := replaceOrAppend(tree.Entries, leaf)
		return storeTree(s, newEntries)
	}
	dir := parts[0]
	rest := parts[1:]
	var child *object.Tree
	if existing := findEntryInSlice(tree.Entries, dir); existing != nil && existing.Mode == filemode.Dir {
		loaded, err := loadTree(s, existing.Hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		child = loaded
	} else {
		child, _ = emptyTree(s) // new directory
	}
	childHash, err := upsertEntry(s, child, rest, leaf)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	dirEntry := object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: childHash}
	newEntries := replaceOrAppend(tree.Entries, dirEntry)
	return storeTree(s, newEntries)
}

// removeFileFromTree is the delete-side counterpart of upsertFileInTree
// and likewise preserves every sibling entry at every tree level.
func removeFileFromTree(s storer.EncodedObjectStorer, root *object.Tree, filePath string) (plumbing.Hash, error) {
	parts := splitPath(filePath)
	return removeEntry(s, root, parts)
}

func removeEntry(s storer.EncodedObjectStorer, tree *object.Tree, parts []string) (plumbing.Hash, error) {
	if len(parts) == 1 {
		newEntries := removeEntryByName(tree.Entries, parts[0])
		return storeTree(s, newEntries)
	}
	dir := parts[0]
	existing := findEntryInSlice(tree.Entries, dir)
	if existing == nil || existing.Mode != filemode.Dir {
		return plumbing.ZeroHash, errFileNotFound(joinPath(parts))
	}
	child, err := loadTree(s, existing.Hash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	childHash, err := removeEntry(s, child, parts[1:])
	if err != nil {
		return plumbing.ZeroHash, err
	}
	// If the directory became empty, prune it — matches git's behaviour
	// and keeps the tree tidy.
	if childHash == emptyTreeHash {
		newEntries := removeEntryByName(tree.Entries, dir)
		return storeTree(s, newEntries)
	}
	dirEntry := object.TreeEntry{Name: dir, Mode: filemode.Dir, Hash: childHash}
	newEntries := replaceOrAppend(tree.Entries, dirEntry)
	return storeTree(s, newEntries)
}

// replaceOrAppend returns a copy of entries with any same-named entry
// replaced by neu; otherwise neu appended. Sort order is preserved.
func replaceOrAppend(entries []object.TreeEntry, neu object.TreeEntry) []object.TreeEntry {
	out := make([]object.TreeEntry, 0, len(entries)+1)
	replaced := false
	for _, e := range entries {
		if e.Name == neu.Name {
			out = append(out, neu)
			replaced = true
			continue
		}
		out = append(out, e)
	}
	if !replaced {
		out = append(out, neu)
	}
	sortTreeEntries(out)
	return out
}

func removeEntryByName(entries []object.TreeEntry, name string) []object.TreeEntry {
	out := make([]object.TreeEntry, 0, len(entries))
	for _, e := range entries {
		if e.Name != name {
			out = append(out, e)
		}
	}
	return out
}

func findEntryInSlice(entries []object.TreeEntry, name string) *object.TreeEntry {
	for i := range entries {
		if entries[i].Name == name {
			return &entries[i]
		}
	}
	return nil
}

// findEntry resolves a slash path against a tree using go-git's built-in
// FindEntry, which walks subtrees for us. Returns nil when not present.
func findEntry(tree *object.Tree, filePath string) *object.TreeEntry {
	if tree == nil {
		return nil
	}
	entry, err := tree.FindEntry(filePath)
	if err != nil {
		return nil
	}
	return entry
}

// --- low-level storer helpers ----------------------------------------------

// writeBlob stores content as a blob and returns (hash, size).
func writeBlob(s storer.EncodedObjectStorer, content []byte) (plumbing.Hash, int64, error) {
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(content)))
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, 0, err
	}
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, 0, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, 0, err
	}
	hash, err := s.SetEncodedObject(obj)
	if err != nil {
		return plumbing.ZeroHash, 0, err
	}
	return hash, int64(len(content)), nil
}

// storeTree encodes entries as a tree object, stores it, returns hash.
func storeTree(s storer.EncodedObjectStorer, entries []object.TreeEntry) (plumbing.Hash, error) {
	tree := &object.Tree{Entries: entries}
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.TreeObject)
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(obj)
}

// loadTree reads a tree object by hash.
func loadTree(s storer.EncodedObjectStorer, hash plumbing.Hash) (*object.Tree, error) {
	obj, err := s.EncodedObject(plumbing.TreeObject, hash)
	if err != nil {
		return nil, err
	}
	return object.DecodeTree(s, obj)
}

// emptyTree returns an in-memory empty tree used as a starting point
// for upsertEntry when materialising a new directory. It is never stored
// directly; upsertEntry always re-encodes its entries into a fresh tree
// object via storeTree, so we don't need to attach the storer here.
func emptyTree(s storer.EncodedObjectStorer) (*object.Tree, error) {
	_ = s
	return &object.Tree{Entries: []object.TreeEntry{}}, nil
}

// emptyTreeHash is the canonical git hash of an empty tree object.
var emptyTreeHash = plumbing.NewHash("4b825dc642cb6eb9a060e54bf8d69288fbee4904")

// commitTreeFromParents creates a commit object pointing at treeHash
// with the given parents, message and committer identity. Author is
// set equal to committer — the editor is the only writer here.
func commitTreeFromParents(s storer.EncodedObjectStorer, treeHash plumbing.Hash, parents []plumbing.Hash, message, committerName, committerEmail string) (plumbing.Hash, error) {
	commit := &object.Commit{
		Author:    object.Signature{Name: committerName, Email: committerEmail, When: time.Now()},
		Committer: object.Signature{Name: committerName, Email: committerEmail, When: time.Now()},
		Message:   message,
		TreeHash:  treeHash,
	}
	commit.ParentHashes = parents
	obj := s.NewEncodedObject()
	obj.SetType(plumbing.CommitObject)
	if err := commit.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return s.SetEncodedObject(obj)
}

// setBranchRef moves refs/heads/<branch> from oldHash to newHash. We
// use CheckAndSetReference so a concurrent push landing between our
// read and write fails our write instead of silently fast-forwarding
// over the other side's history.
func setBranchRef(s storer.ReferenceStorer, name plumbing.ReferenceName, newHash, oldHash plumbing.Hash) error {
	newRef := plumbing.NewHashReference(name, newHash)
	var oldRef *plumbing.Reference
	if oldHash != plumbing.ZeroHash {
		oldRef = plumbing.NewHashReference(name, oldHash)
	}
	return s.CheckAndSetReference(newRef, oldRef)
}

// --- path + sort helpers ---------------------------------------------------

func splitPath(p string) []string {
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func joinPath(parts []string) string { return strings.Join(parts, "/") }

func isSkillMetadataPath(p string) bool {
	return path.Clean(strings.TrimSpace(p)) == "SKILL.md"
}

func isDefaultBranchName(branch string) bool {
	branch = strings.TrimPrefix(strings.TrimSpace(branch), "refs/heads/")
	if branch == "" {
		branch = biz.SkillDefaultBranch
	}
	return branch == biz.SkillDefaultBranch
}

func sortTreeEntries(entries []object.TreeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return treeEntryLess(entries[i], entries[j])
	})
}

// treeEntryLess mirrors git's canonical tree sort: directories sort as
// if they had a trailing slash, so "src" (dir) < "src.go" (file).
func treeEntryLess(a, b object.TreeEntry) bool {
	an := a.Name
	bn := b.Name
	if a.Mode == filemode.Dir {
		an += "/"
	}
	if b.Mode == filemode.Dir {
		bn += "/"
	}
	return an < bn
}

// --- soft-serve entry conversion ------------------------------------------

// entryToFileInfo converts a Soft Serve TreeEntry into a biz.FileInfo.
// parentPath is the path of the owning tree ("" for root) so we can
// compute the entry's full path without reaching into the SDK's
// unexported TreeEntry.path field.
func entryToFileInfo(entry *softgit.TreeEntry, parentPath, ref string) (*biz.FileInfo, error) {
	typ := "file"
	mode := entry.Mode().String()
	size := int64(0)
	if entry.IsTree() {
		typ = "dir"
	} else if entry.IsCommit() {
		typ = "commit"
	} else {
		if s, err := entrySize(entry); err == nil {
			size = s
		}
	}
	fullPath := entry.Name()
	if parentPath != "" {
		fullPath = parentPath + "/" + entry.Name()
	}
	return &biz.FileInfo{
		Name: entry.Name(), Path: fullPath, Type: typ, Size: size,
		Mode: mode, SHA: entry.ID().String(),
	}, nil
}

func entrySize(entry *softgit.TreeEntry) (int64, error) {
	if entry.IsTree() {
		return 0, nil
	}
	f := entry.File()
	if f == nil {
		return 0, nil
	}
	return f.Size(), nil
}

// isNoSuchRef / isNoSuchPath detect Soft Serve's sentinel errors. They
// are string-matched because the SDK wraps git-module errors without
// exporting typed sentinels we can errors.Is against.
func isNoSuchRef(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "reference not found") ||
		strings.Contains(msg, "rev not found") ||
		strings.Contains(msg, "unknown revision")
}

func isNoSuchPath(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such file") ||
		strings.Contains(msg, "path not found")
}

// --- base64 ---------------------------------------------------------------

// encodeBase64 returns the standard base64 encoding of b. The proto
// FileContent.content field is declared base64, so we actually encode
// (aisphere-git-server returned plaintext here — a bug we do not copy).
func encodeBase64(b []byte) string {
	if b == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(b)
}
