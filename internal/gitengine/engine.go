package gitengine

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	softgit "github.com/aisphereio/soft-serve/git"
	"github.com/aisphereio/soft-serve/pkg/backend"
	"github.com/aisphereio/soft-serve/pkg/config"
	"github.com/aisphereio/soft-serve/pkg/db"
	"github.com/aisphereio/soft-serve/pkg/db/migrate"
	"github.com/aisphereio/soft-serve/pkg/db/models"
	softproto "github.com/aisphereio/soft-serve/pkg/proto"
	"github.com/aisphereio/soft-serve/pkg/store"
	"github.com/aisphereio/soft-serve/pkg/store/database"
	softweb "github.com/aisphereio/soft-serve/pkg/web"
	"gorm.io/gorm"
)

const ZeroHash = "0000000000000000000000000000000000000000"

const postgresDriver = "postgres"

type Config struct {
	DataPath      string
	IAMEndpoint   string
	IAMInsecure   bool
	IAMCaller     string
	DefaultBranch string
}

type Engine struct {
	cfg     *config.Config
	db      *db.DB
	store   store.Store
	backend *backend.Backend
	owner   softproto.User
	handler http.Handler
}

// New embeds Soft Serve using the PostgreSQL connection pool already owned by
// Kernel/dbx. The engine deliberately does not create or close a second pool.
func New(ctx context.Context, in Config, gormDB *gorm.DB) (*Engine, error) {
	return newWithDatabase(ctx, in, gormDB, postgresDriver)
}

func newWithDatabase(ctx context.Context, in Config, gormDB *gorm.DB, driverName string) (*Engine, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if gormDB == nil {
		return nil, fmt.Errorf("gitengine: Kernel database is required")
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("gitengine: resolve Kernel SQL database: %w", err)
	}
	driverName = strings.TrimSpace(driverName)
	if driverName == "" {
		return nil, fmt.Errorf("gitengine: database driver is required")
	}

	dataPath := strings.TrimSpace(in.DataPath)
	if dataPath == "" {
		dataPath = filepath.Join("data", "git")
	}
	if err := os.MkdirAll(dataPath, 0o755); err != nil {
		return nil, fmt.Errorf("gitengine: create data path: %w", err)
	}

	cfg := config.DefaultConfig()
	cfg.DataPath = dataPath
	cfg.DB.Driver = driverName
	cfg.DB.DataSource = ""
	engineCtx := config.WithContext(ctx, cfg)

	// Kernel owns sqlDB. Soft Serve only wraps it with sqlx and therefore must
	// not close it when the embedded engine stops.
	dbx, err := db.NewWithSQLDB(engineCtx, sqlDB, driverName, false)
	if err != nil {
		return nil, fmt.Errorf("gitengine: wrap Kernel database: %w", err)
	}
	if err := migrate.Migrate(engineCtx, dbx); err != nil {
		return nil, fmt.Errorf("gitengine: migrate metadata database: %w", err)
	}

	st := database.New(engineCtx, dbx)
	engineCtx = db.WithContext(engineCtx, dbx)
	engineCtx = store.WithContext(engineCtx, st)
	be := backend.New(engineCtx, cfg, dbx, st)
	engineCtx = backend.WithContext(engineCtx, be)
	engineCtx = softweb.WithProtocolEnvironment(engineCtx, hookEnvironment(in))
	defaultBranch := strings.TrimSpace(in.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = biz.SkillDefaultBranch
	}
	engineCtx = softweb.WithProtocolDefaultBranch(engineCtx, defaultBranch)
	owner, err := be.User(engineCtx, "skillhub")
	if err != nil {
		owner, err = be.CreateUser(engineCtx, "skillhub", softproto.UserOptions{Admin: true})
	}
	if err != nil {
		return nil, fmt.Errorf("gitengine: initialize repository owner: %w", err)
	}
	return &Engine{cfg: cfg, db: dbx, store: st, backend: be, owner: owner, handler: softweb.NewProtocolRouter(engineCtx)}, nil
}

func hookEnvironment(cfg Config) softweb.ProtocolEnvironment {
	return func(ctx context.Context) []string {
		principal, _ := authn.PrincipalFromContext(ctx)
		principal = principal.Normalize()
		defaultBranch := strings.TrimSpace(cfg.DefaultBranch)
		if defaultBranch == "" {
			defaultBranch = biz.SkillDefaultBranch
		}
		return []string{
			"AISPHERE_PRINCIPAL_ID=" + principal.SubjectID,
			"AISPHERE_PRINCIPAL_TYPE=" + principal.SubjectType,
			"AISPHERE_PRINCIPAL_TENANT_ID=" + principal.TenantID,
			"AISPHERE_PRINCIPAL_ORG_ID=" + principal.OrgID,
			"AISPHERE_IAM_AUTHZ_ENDPOINT=" + strings.TrimSpace(cfg.IAMEndpoint),
			"AISPHERE_IAM_AUTHZ_INSECURE=" + strconv.FormatBool(cfg.IAMInsecure),
			"AISPHERE_IAM_CALLER_SERVICE=" + strings.TrimSpace(cfg.IAMCaller),
			"AISPHERE_GIT_DEFAULT_BRANCH=" + defaultBranch,
		}
	}
}

// Close releases engine-local resources. The shared SQL pool remains owned by
// Kernel; db.Close is a no-op for the non-owning wrapper.
func (e *Engine) Close() error {
	if e == nil || e.db == nil {
		return nil
	}
	return e.db.Close()
}

func (e *Engine) Handler() http.Handler {
	if e == nil {
		return http.NotFoundHandler()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		described, err := softweb.DescribeRequest(r)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimSuffix(strings.TrimSpace(described.Repository), ".git")
		status, err := e.lifecycleStatus(r.Context(), name)
		if errors.Is(err, sql.ErrNoRows) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "repository state unavailable", http.StatusServiceUnavailable)
			return
		}
		if status != biz.SkillStatusActive {
			http.Error(w, "repository is not active", http.StatusConflict)
			return
		}
		e.handler.ServeHTTP(w, r)
	})
}

// CreateSkill creates the canonical Soft Serve repository row and the Hub
// profile row in the same PostgreSQL transaction. Git filesystem creation is
// part of Soft Serve's repository lifecycle and remains compensated by its
// backend when the transaction fails.
func (e *Engine) CreateSkill(ctx context.Context, skill *biz.GitSkill) (*biz.GitSkill, error) {
	if skill == nil {
		return nil, biz.ErrSkillInvalidArgument
	}
	item := *skill
	item.Name = strings.TrimSpace(item.Name)
	item.OrgID = strings.TrimSpace(item.OrgID)
	item.ProjectID = strings.TrimSpace(item.ProjectID)
	if item.DefaultBranch == "" {
		item.DefaultBranch = biz.SkillDefaultBranch
	}
	if item.Visibility == "" {
		item.Visibility = biz.SkillVisibilityPrivate
	}
	if item.Status == "" {
		item.Status = biz.SkillStatusProvisioning
	}
	if item.OwnerType == "" {
		item.OwnerType = authn.SubjectTypeUser
	}

	var canonical models.Repo
	opCtx := store.WithRepositoryCreateExtension(e.operationContext(ctx), func(ctx context.Context, tx db.Handler, repo models.Repo) error {
		canonical = repo
		query := tx.Rebind(`INSERT INTO hub_skill_profiles (
			repository_id, display_name, org_id, project_id,
			created_by_type, created_by_id, visibility, lifecycle_status,
			default_branch, provision_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, '', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`)
		_, err := tx.ExecContext(ctx, query,
			repo.ID, item.DisplayName, item.OrgID, item.ProjectID,
			item.OwnerType, item.OwnerID, item.Visibility, item.Status, item.DefaultBranch,
		)
		return err
	})
	_, err := e.backend.CreateRepository(opCtx, item.Name, e.owner, softproto.RepositoryOptions{
		ProjectName: item.ProjectID,
		Description: item.Description,
		Private:     item.Visibility != biz.SkillVisibilityPublic,
		LFS:         true,
	})
	if err != nil {
		if errors.Is(err, softproto.ErrRepoExist) {
			return nil, biz.ErrSkillAlreadyExists
		}
		return nil, fmt.Errorf("%w: create repository: %v", biz.ErrSkillDependencyFailed, err)
	}
	item.RepositoryID = canonical.ID
	item.CreateTime = canonical.CreatedAt
	item.UpdateTime = canonical.UpdatedAt

	// Seed an initial commit on the default branch so the skill is immediately
	// cloneable, has a materialized HEAD, and can host pull requests. The biz
	// layer does not compensate when CreateSkill fails, so on seed error we
	// delete the half-created repository to avoid orphans.
	if seedErr := e.seedInitialCommit(ctx, item.Name, &item); seedErr != nil {
		compensateCtx := context.WithoutCancel(ctx)
		_ = e.backend.DeleteRepository(e.operationContext(compensateCtx), item.Name)
		return nil, fmt.Errorf("%w: seed initial commit: %v", biz.ErrSkillDependencyFailed, seedErr)
	}

	return &item, nil
}

func (e *Engine) DeleteRepository(ctx context.Context, name string) error {
	return e.backend.DeleteRepository(softproto.WithUserContext(e.operationContext(ctx), e.owner), name)
}

// seedInitialCommit writes an initial commit (SKILL.md) to the
// default branch of a freshly created skill repository and points HEAD at it.
// It uses raw git plumbing (hash-object / mktree / commit-tree / update-ref /
// symbolic-ref) against the bare repo path, mirroring the Merge helper's
// exec-against-repo.Path pattern. soft-serve/git-module exposes no high-level
// bare-repo commit API (its Commit runs `git commit`, which needs an index).
func (e *Engine) seedInitialCommit(ctx context.Context, name string, skill *biz.GitSkill) error {
	repo, err := e.open(ctx, name)
	if err != nil {
		return err
	}
	gitDir := repo.Path
	skillMd := scaffoldContent(skill)

	mdSha, err := runGitRepo(ctx, gitDir, strings.NewReader(skillMd), "hash-object", "-w", "--stdin")
	if err != nil {
		return fmt.Errorf("write SKILL.md blob: %w", err)
	}
	// mktree reads "<mode> <type> <sha>\t<name>" lines from stdin.
	treeInput := fmt.Sprintf("100644 blob %s\tSKILL.md\n", mdSha)
	treeSha, err := runGitRepo(ctx, gitDir, strings.NewReader(treeInput), "mktree")
	if err != nil {
		return fmt.Errorf("build tree: %w", err)
	}

	commitMsg := fmt.Sprintf("Initial scaffold\n\nCreated by %s", skill.OwnerID)
	now := time.Now().UTC().Format(time.RFC3339)
	env := []string{
		"GIT_AUTHOR_NAME=Aisphere Hub", "GIT_AUTHOR_EMAIL=noreply@aisphere.io",
		"GIT_AUTHOR_DATE=" + now,
		"GIT_COMMITTER_NAME=Aisphere Hub", "GIT_COMMITTER_EMAIL=noreply@aisphere.io",
		"GIT_COMMITTER_DATE=" + now,
	}
	commitSha, err := runGitRepoEnv(ctx, gitDir, nil, env, "commit-tree", treeSha, "-m", commitMsg)
	if err != nil {
		return fmt.Errorf("create commit: %w", err)
	}

	branch := biz.SkillDefaultBranch // "main"
	if _, err := runGitRepo(ctx, gitDir, nil, "update-ref", "refs/heads/"+branch, commitSha); err != nil {
		return fmt.Errorf("update %s ref: %w", branch, err)
	}
	if _, err := runGitRepo(ctx, gitDir, nil, "symbolic-ref", "HEAD", "refs/heads/"+branch); err != nil {
		return fmt.Errorf("point HEAD at %s: %w", branch, err)
	}
	return nil
}

// runGitRepo runs a git plumbing command against a bare repo path (GIT_DIR)
// and returns trimmed stdout. stdin may be nil.
func runGitRepo(ctx context.Context, gitDir string, stdin io.Reader, args ...string) (string, error) {
	return runGitRepoEnv(ctx, gitDir, stdin, nil, args...)
}

// runGitRepoEnv is runGitRepo with extra environment variables (for
// GIT_AUTHOR_*/GIT_COMMITTER_* identity on commit-tree).
func runGitRepoEnv(ctx context.Context, gitDir string, stdin io.Reader, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--git-dir", gitDir}, args...)...)
	if stdin != nil {
		cmd.Stdin = stdin
	}
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = strings.TrimSpace(string(ee.Stderr))
		}
		return "", fmt.Errorf("git %s: %w%s", strings.Join(args, " "), err, nonemptyPrefix(stderr))
	}
	return strings.TrimSpace(string(out)), nil
}

func nonemptyPrefix(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func (e *Engine) ResolveRef(ctx context.Context, name, ref string) (string, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return "", err
	}
	return repo.ShowRefVerify(normalizeRef(ref))
}

// Merge advances target to source only when source is a descendant of target.
// update-ref includes the caller's expected target SHA, making the merge atomic.
func (e *Engine) Merge(ctx context.Context, name, sourceRef, targetRef, expectedTargetSHA string) (string, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return "", err
	}
	sourceRef, targetRef = normalizeRef(sourceRef), normalizeRef(targetRef)
	sourceSHA, err := repo.ShowRefVerify(sourceRef)
	if err != nil {
		return "", err
	}
	mergeBase, err := repo.MergeBase(expectedTargetSHA, sourceSHA)
	if err != nil {
		return "", fmt.Errorf("gitengine: merge base: %w", err)
	}
	if strings.TrimSpace(mergeBase) != strings.TrimSpace(expectedTargetSHA) {
		return "", biz.ErrPullRequestStale
	}
	if _, _, err := e.readSkillMetadata(ctx, repo, sourceSHA, name); err != nil {
		return "", fmt.Errorf("gitengine: validate SKILL.md: %w", err)
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repo.Path, "update-ref", targetRef, sourceSHA, expectedTargetSHA)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("gitengine: update target ref: %w: %s", err, strings.TrimSpace(string(output)))
	}
	if err := e.syncSkillMetadata(ctx, name, sourceSHA, repo.Path); err != nil {
		return "", err
	}
	return sourceSHA, nil
}

// SyncSkillMetadata refreshes the PostgreSQL metadata projection from the
// default branch. The Git document is authoritative; the Hub columns remain
// a query cache for list/detail APIs.
func (e *Engine) SyncSkillMetadata(ctx context.Context, name, ref string) error {
	repo, err := e.open(ctx, name)
	if err != nil {
		return err
	}
	sha, err := repo.ShowRefVerify(normalizeRef(ref))
	if err != nil {
		return err
	}
	return e.syncSkillMetadata(ctx, name, sha, repo.Path)
}

func (e *Engine) readSkillMetadata(ctx context.Context, repo *softgit.Repository, sha, name string) (string, string, error) {
	content, err := runGitRepo(ctx, repo.Path, nil, "show", strings.TrimSpace(sha)+":SKILL.md")
	if err != nil {
		return "", "", err
	}
	displayName, description, err := ParseSkillMetadata(name, content)
	return displayName, description, err
}

func (e *Engine) syncSkillMetadata(ctx context.Context, name, sha, repoPath string) error {
	content, err := runGitRepo(ctx, repoPath, nil, "show", strings.TrimSpace(sha)+":SKILL.md")
	if err != nil {
		return fmt.Errorf("gitengine: read SKILL.md: %w", err)
	}
	displayName, description, err := ParseSkillMetadata(name, content)
	if err != nil {
		return fmt.Errorf("gitengine: validate SKILL.md: %w", err)
	}
	query := e.db.Rebind(`UPDATE hub_skill_profiles
		SET display_name = ?, updated_at = CURRENT_TIMESTAMP
		WHERE repository_id = (SELECT id FROM repos WHERE name = ?)`)
	if _, err := e.db.ExecContext(ctx, query, displayName, name); err != nil {
		return fmt.Errorf("gitengine: update skill metadata projection: %w", err)
	}
	query = e.db.Rebind(`UPDATE repos SET description = ?, updated_at = CURRENT_TIMESTAMP WHERE name = ?`)
	if _, err := e.db.ExecContext(ctx, query, description, name); err != nil {
		return fmt.Errorf("gitengine: update repository metadata projection: %w", err)
	}
	return nil
}

func (e *Engine) ListReleases(ctx context.Context, name string) ([]biz.SkillRelease, error) {
	repo, err := e.open(ctx, name)
	if err != nil {
		return nil, err
	}
	tags, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	out := make([]biz.SkillRelease, 0, len(tags))
	for _, tag := range tags {
		commit, err := repo.TagCommit(tag)
		if err != nil {
			return nil, err
		}
		out = append(out, biz.SkillRelease{Tag: tag, CommitSHA: commit.ID.String(), CreateTime: commit.Committer.When})
	}
	return out, nil
}

func (e *Engine) open(ctx context.Context, name string) (*softgit.Repository, error) {
	item, err := e.backend.Repository(e.operationContext(ctx), name)
	if err != nil {
		return nil, err
	}
	repo, err := item.Open()
	if err != nil {
		return nil, err
	}
	return repo, nil
}

func (e *Engine) lifecycleStatus(ctx context.Context, name string) (string, error) {
	var status string
	query := e.db.Rebind(`SELECT p.lifecycle_status
		FROM hub_skill_profiles p
		JOIN repos r ON r.id = p.repository_id
		WHERE r.name = ?`)
	err := e.db.GetContext(ctx, &status, query, strings.TrimSpace(name))
	return status, err
}

func (e *Engine) operationContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = config.WithContext(ctx, e.cfg)
	ctx = db.WithContext(ctx, e.db)
	ctx = store.WithContext(ctx, e.store)
	return backend.WithContext(ctx, e.backend)
}

func normalizeRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "refs/") {
		return ref
	}
	return "refs/heads/" + ref
}

func RequiredPermissionForRefUpdate(defaultBranch, ref, oldSHA, newSHA string, fastForward bool) string {
	defaultRef := "refs/heads/" + strings.TrimPrefix(strings.TrimSpace(defaultBranch), "refs/heads/")
	if strings.HasPrefix(ref, "refs/tags/") {
		if isZero(oldSHA) && !isZero(newSHA) {
			return "publish"
		}
		return "manage"
	}
	if ref == defaultRef {
		if isZero(newSHA) || (!isZero(oldSHA) && !fastForward) {
			return "manage"
		}
		return "publish"
	}
	return "edit"
}

func isZero(hash string) bool {
	hash = strings.TrimSpace(hash)
	return hash == "" || strings.Trim(hash, "0") == ""
}
