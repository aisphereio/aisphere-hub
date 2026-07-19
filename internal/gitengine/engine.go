package gitengine

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/authn"
	softgit "github.com/aisphereio/soft-serve/git"
	"github.com/aisphereio/soft-serve/pkg/backend"
	"github.com/aisphereio/soft-serve/pkg/config"
	"github.com/aisphereio/soft-serve/pkg/db"
	"github.com/aisphereio/soft-serve/pkg/db/migrate"
	softproto "github.com/aisphereio/soft-serve/pkg/proto"
	"github.com/aisphereio/soft-serve/pkg/store"
	"github.com/aisphereio/soft-serve/pkg/store/database"
	softweb "github.com/aisphereio/soft-serve/pkg/web"
)

const ZeroHash = "0000000000000000000000000000000000000000"

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

func New(ctx context.Context, in Config) (*Engine, error) {
	if ctx == nil {
		ctx = context.Background()
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
	cfg.DB.Driver = "sqlite"
	cfg.DB.DataSource = filepath.Join(dataPath, "soft-serve.db")
	engineCtx := config.WithContext(ctx, cfg)
	dbx, err := db.Open(engineCtx, cfg.DB.Driver, cfg.DB.DataSource)
	if err != nil {
		return nil, fmt.Errorf("gitengine: open metadata database: %w", err)
	}
	if err := migrate.Migrate(engineCtx, dbx); err != nil {
		_ = dbx.Close()
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
		_ = dbx.Close()
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
	return e.handler
}

func (e *Engine) CreateRepository(ctx context.Context, name string) error {
	_, err := e.backend.CreateRepository(e.operationContext(ctx), name, e.owner, softproto.RepositoryOptions{Private: true, LFS: true})
	return err
}

func (e *Engine) DeleteRepository(ctx context.Context, name string) error {
	return e.backend.DeleteRepository(softproto.WithUserContext(e.operationContext(ctx), e.owner), name)
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
	cmd := exec.CommandContext(ctx, "git", "--git-dir", repo.Path, "update-ref", targetRef, sourceSHA, expectedTargetSHA)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("gitengine: update target ref: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return sourceSHA, nil
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
