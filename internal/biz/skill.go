// Package biz skill module — canonical Skill + SkillVersion + SkillFile
// usecase migrated from the legacy backend.
//
// Layering contract:
//   - biz imports: kernel logx + errorx + authn (Principal only), internal/skillzip
//   - biz MUST NOT import: data, conf, gorm, kernel dbx/cachex/objectstorex
//
// The usecase layer validates inputs, enforces business invariants (name
// format, version state machine), records audit-friendly logs via logx, and
// delegates persistence to SkillRepo. Authorization is intentionally thin
// in this first migration: ListSkills / GetSkill fall back to ownership +
// public-visibility when no explicit grant exists. A later phase will
// integrate accessx.Guard for fine-grained share management.

package biz

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/aisphereio/aisphere-hub/internal/observability"

	"github.com/aisphereio/kernel/auditx"
	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
	"github.com/aisphereio/kernel/metricsx"
)

// --- Domain constants ---

const (
	SkillStatusActive   = "active"
	SkillStatusArchived = "archived"

	SkillVisibilityPrivate = "private"
	SkillVisibilityPublic  = "public"

	// SkillVersionStatus* are the canonical states for a SkillVersion.
	// State machine:
	//   draft → submitted → published → online → offline
	//                                       ↑________|
	// online → offline is reversible (offline → published → online).
	SkillVersionStatusDraft     = "draft"
	SkillVersionStatusSubmitted = "submitted"
	SkillVersionStatusPublished = "published"
	SkillVersionStatusOnline    = "online"
	SkillVersionStatusOffline   = "offline"

	// DefaultSkillVersion is used when CreateSkill is called without an
	// explicit version. It matches skillzip.DefaultInitialVersion so an
	// upload right after create lands on the same version string.
	DefaultSkillVersion = "0.0.1"
)

// --- Error sentinels ---
//
// We use kernel errorx so the service layer gets HTTP status mapping for
// free (NotFound → 404, Conflict → 409, BadRequest → 400). Each error
// carries a stable Code that frontend / SDK code can switch on without
// parsing the message.

var (
	ErrSkillNotFound = errorx.NotFound(
		errorx.Code("SKILL_NOT_FOUND"),
		"skill not found",
	)
	ErrSkillAlreadyExists = errorx.Conflict(
		errorx.Code("SKILL_ALREADY_EXISTS"),
		"skill already exists",
	)
	ErrSkillVersionNotFound = errorx.NotFound(
		errorx.Code("SKILL_VERSION_NOT_FOUND"),
		"skill version not found",
	)
	ErrSkillVersionAlreadyExists = errorx.Conflict(
		errorx.Code("SKILL_VERSION_ALREADY_EXISTS"),
		"skill version already exists",
	)
	ErrSkillFileNotFound = errorx.NotFound(
		errorx.Code("SKILL_FILE_NOT_FOUND"),
		"skill file not found",
	)
	ErrSkillVersionNotDraft = errorx.BadRequest(
		errorx.Code("SKILL_VERSION_NOT_DRAFT"),
		"skill version is not draft",
	)
	ErrSkillInvalidArgument = errorx.BadRequest(
		errorx.Code("SKILL_INVALID_ARGUMENT"),
		"invalid skill argument",
	)
	ErrSkillPackageInvalid = errorx.BadRequest(
		errorx.Code("SKILL_PACKAGE_INVALID"),
		"invalid skill package",
	)
	ErrSkillPermissionDenied = errorx.Forbidden(
		errorx.Code("SKILL_PERMISSION_DENIED"),
		"permission denied",
	)

	// skillNameRE matches the canonical Skill name format: alphanumeric
	// first char, then up to 127 chars of [A-Za-z0-9_.-]. Compiled once at
	// init; ValidateSkillName is hot path (called from every CRUD RPC).
	skillNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// --- Domain types ---

// Skill is the canonical Skill entity.
type Skill struct {
	ID           int64
	Name         string
	DisplayName  string
	Description  string
	Version      string
	Status       string
	Visibility   string
	OwnerID      string
	OrgID        string
	ProjectID    string
	SourceType   string
	SourceURI    string
	ManifestJSON string
	Tags         []string
	CreateTime   time.Time
	UpdateTime   time.Time
}

// SkillVersion is one versioned snapshot of a Skill's package.
type SkillVersion struct {
	ID                  int64
	SkillName           string
	Version             string
	Status              string
	Author              string
	CommitMsg           string
	PublishPipelineInfo string
	DownloadCount       int64
	MD5                 string
	SHA256              string
	Revision            string
	SizeBytes           int64
	ManifestJSON        string
	CreateTime          time.Time
	UpdateTime          time.Time
}

// SkillFile is one file inside a SkillVersion's package.
type SkillFile struct {
	ID         int64
	SkillName  string
	Version    string
	Path       string
	Name       string
	Type       string
	Size       int64
	Binary     bool
	Content    string
	CreateTime time.Time
	UpdateTime time.Time
}

// SkillPackageDownload is the result of DownloadSkillVersion. ETag + MD5 +
// SHA256 let clients do conditional GETs.
type SkillPackageDownload struct {
	SkillName    string
	Version      string
	ETag         string
	MD5          string
	SHA256       string
	NotModified  bool
	PackageBytes []byte
}

// SkillPackageUpload is the input for UploadSkillPackage.
type SkillPackageUpload struct {
	PackageBytes  []byte
	Overwrite     bool
	TargetVersion string
	CommitMsg     string
}

// SkillListOptions controls ListSkills filtering and pagination.
type SkillListOptions struct {
	Limit      int
	Offset     int
	Query      string
	Status     string
	Visibility string
	OnlyOnline bool
}

// SkillListResult is the paginated result of ListSkills. NextOffset is
// the offset to use for the next page; HasMore is false when the current
// page returned fewer than Limit items.
type SkillListResult struct {
	Items      []*Skill
	NextOffset int
	HasMore    bool
}

// --- SkillRepo interface ---
//
// The repo abstracts persistence so biz can be unit-tested with a fake.
// Implementations live in internal/data and use kernel dbx.DB.

type SkillRepo interface {
	CreateSkill(ctx context.Context, skill *Skill) (*Skill, error)
	UpdateSkill(ctx context.Context, skill *Skill) (*Skill, error)
	UpdateSkillVisibility(ctx context.Context, name, visibility string) (*Skill, error)
	ListSkills(ctx context.Context, opts SkillListOptions) (*SkillListResult, error)
	GetSkill(ctx context.Context, name string) (*Skill, error)
	DeleteSkill(ctx context.Context, name string) error

	ListSkillVersions(ctx context.Context, name string) ([]*SkillVersion, error)
	GetSkillVersion(ctx context.Context, name, version string) (*SkillVersion, error)
	UpdateSkillVersionStatus(ctx context.Context, name, version, expectedStatus, newStatus string) (*SkillVersion, error)
	GetOnlineSkillVersion(ctx context.Context, name string) (*SkillVersion, error)

	ListSkillVersionFiles(ctx context.Context, name, version string) ([]*SkillFile, error)
	GetSkillVersionFile(ctx context.Context, name, version, filePath string) (*SkillFile, error)

	// Draft workspace operations are used by the online skill editor. Draft
	// files are persisted immediately (S3 object + PG metadata), so browser
	// refreshes do not lose newly-created text, files, or directories.
	ListSkillDraftFiles(ctx context.Context, name, version string) ([]*SkillFile, error)
	GetSkillDraftFile(ctx context.Context, name, version, filePath string) (*SkillFile, error)
	UpsertSkillDraftFile(ctx context.Context, file *SkillFile, actor string) (*SkillFile, error)
	DeleteSkillDraftPath(ctx context.Context, name, version, filePath string, recursive bool) error
	MoveSkillDraftPath(ctx context.Context, name, version, oldPath, newPath string, overwrite bool) error
	BuildSkillPackageFromDraft(ctx context.Context, name, version string) ([]byte, []*SkillFile, error)

	// SaveSkillPackage stores the package content in object storage and
	// persists only PG control metadata (skill/version rows, object keys,
	// hashes, manifest pointer). Implementations may use DTM Saga to keep
	// S3 promotion and PG metadata writes eventually consistent.
	SaveSkillPackage(ctx context.Context, skill *Skill, version *SkillVersion, files []*SkillFile, packageBytes []byte, overwrite bool) (*SkillVersion, error)

	// DownloadSkillPackage returns the package bytes for one version. If
	// ifNoneMatch equals the version's ETag (sha256), returns
	// (SkillPackageDownload{NotModified: true}, nil) without reading the
	// object body.
	DownloadSkillPackage(ctx context.Context, name, version, ifNoneMatch string) (*SkillPackageDownload, error)

	// IncrementSkillVersionDownloadCount atomically bumps download_count.
	// Best-effort: callers should not fail the download if this fails.
	IncrementSkillVersionDownloadCount(ctx context.Context, name, version string) error
}

// --- Usecase ---

// SkillUsecase orchestrates Skill / SkillVersion / SkillFile operations.
//
// Authz integration:
//
//   - CreateSkill:  after the row is persisted, writes a
//     skill:{name}#owner@user:{uid} relationship so the
//     creator becomes the owner. Failure to write the
//     relationship is logged but does NOT roll back the
//     skill row — the ownership fallback (owner_id field
//     match) still grants access. This is intentional:
//     authz is a policy layer, not a data invariant.
//
//   - GetSkill:     checks skill.read via authz. Falls back to ownership
//     (skill.owner_id == principal.SubjectID) and public
//     visibility (skill.visibility == "public") when authz
//     denies or is unavailable. This keeps the API usable
//     in dev when authz is disabled.
//
//   - ListSkills:   uses authz.LookupResources to compute the visible
//     skill set when authz is enabled; falls back to "all
//     skills" when authz is disabled (dev mode).
//
//   - UpdateSkill / DeleteSkill / UploadSkillPackage / version state
//     transitions: requires skill.edit / skill.delete via
//     authz. No fallback — write operations MUST be
//     authorized. Ownership fallback is intentionally NOT
//     applied here; startup relationship bootstrap repairs
//     missing owner tuples from the durable owner_id field.
type SkillUsecase struct {
	repo    SkillRepo
	authz   *AuthzUsecase
	audit   auditx.Recorder
	log     logx.Logger
	metrics metricsx.Manager
}

// NewSkillUsecase creates a new SkillUsecase. log may be nil — the
// usecase falls back to logx.Noop() in that case (useful for unit tests).
// authz may be nil — when nil, all authz checks are skipped and the
// usecase behaves as if authz is disabled (useful for unit tests and
// for dev configs that have not yet enabled SpiceDB).
// audit may be nil — when nil, audit records are dropped silently.
func NewSkillUsecase(repo SkillRepo, authz *AuthzUsecase, audit auditx.Recorder, log logx.Logger, managers ...metricsx.Manager) *SkillUsecase {
	if log == nil {
		log = logx.Noop()
	}
	if audit == nil {
		audit = auditx.Noop()
	}
	manager := metricsx.Noop()
	if len(managers) > 0 {
		manager = metricsx.Ensure(managers[0])
	}
	observability.RegisterMetrics(manager)
	return &SkillUsecase{repo: repo, authz: authz, audit: audit, log: log.Named("skill"), metrics: manager}
}

func (uc *SkillUsecase) begin(ctx context.Context, principal authn.Principal, operation string, fields ...logx.Field) (context.Context, logx.Logger, time.Time) {
	fields = append(fields,
		logx.Bool("authenticated", principal.IsAuthenticated()),
		logx.String("subject_id", principal.SubjectID),
		logx.String("org_id", principal.OrgID),
	)
	return observability.Begin(ctx, uc.log, "skill", operation, fields...)
}

func (uc *SkillUsecase) end(ctx context.Context, logger logx.Logger, operation string, started time.Time, err error, fields ...logx.Field) {
	observability.End(ctx, logger, uc.metrics, "skill", operation, started, err, fields...)
}

// --- Skill CRUD ---

// CreateSkill creates a canonical Skill. The caller becomes the owner.
// On duplicate name, returns ErrSkillAlreadyExists.
//
// After the row is persisted, writes a skill:{name}#owner@user:{uid}
// relationship via authz so subsequent authz checks (read / edit /
// delete) recognize the creator as owner. Failure to write the
// relationship is logged but does NOT roll back the skill row — see
// SkillUsecase comment for rationale.
func (uc *SkillUsecase) CreateSkill(ctx context.Context, principal authn.Principal, in *Skill) (out *Skill, err error) {
	name := ""
	if in != nil {
		name = in.Name
	}
	ctx, logger, started := uc.begin(ctx, principal, "create", logx.String("name", name))
	defer func() { uc.end(ctx, logger, "create", started, err, logx.String("name", name)) }()
	if in == nil {
		return nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("skill is required"))
	}
	if err := ValidateSkillName(in.Name); err != nil {
		return nil, err
	}
	skill := normalizeSkillForCreate(in)
	// Stamp the owner from the authenticated principal when the request
	// did not supply one. This makes owner_id the authoritative source
	// of truth for "who created this skill" even when authz is disabled.
	if strings.TrimSpace(skill.OwnerID) == "" && principal.IsAuthenticated() {
		skill.OwnerID = principal.SubjectID
		skill.OrgID = firstNonEmptyString(skill.OrgID, principal.OrgID)
	}
	out, err = uc.repo.CreateSkill(ctx, skill)
	if err != nil {
		uc.recordAudit(ctx, principal, "skill.create", auditx.ResultFailure, err.Error(), "skill", skill.Name, nil)
		uc.log.WithContext(ctx).Warn("skill create failed",
			logx.String("name", skill.Name),
			logx.Err(err),
		)
		return nil, err
	}
	// Best-effort authz owner grant. Failure is logged but not returned:
	// the skill row is already persisted; revoking it would surprise the
	// user. The owner_id field match serves as a fallback for read access.
	if uc.authz != nil && principal.IsAuthenticated() {
		if err := uc.authz.GrantOwner(ctx,
			AuthzObjectRef{Type: "skill", ID: out.Name},
			AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		); err != nil {
			uc.log.WithContext(ctx).Warn("skill owner grant failed (authz); falling back to owner_id field",
				logx.String("name", out.Name),
				logx.String("owner", principal.SubjectID),
				logx.Err(err),
			)
		}
	}
	uc.recordAudit(ctx, principal, "skill.create", auditx.ResultSuccess, "", "skill", out.Name, map[string]any{
		"skill_id": out.ID,
		"owner_id": out.OwnerID,
		"version":  out.Version,
	})
	uc.log.WithContext(ctx).Info("skill created",
		logx.String("name", out.Name),
		logx.Int64("id", out.ID),
		logx.String("owner", out.OwnerID),
	)
	return out, nil
}

// UpdateSkill updates mutable fields. owner_id / visibility / status are
// NOT updated here — they require separate lifecycle endpoints. Requires
// skill.edit permission via authz.
func (uc *SkillUsecase) UpdateSkill(ctx context.Context, principal authn.Principal, in *Skill) (out *Skill, err error) {
	name := ""
	if in != nil {
		name = in.Name
	}
	ctx, logger, started := uc.begin(ctx, principal, "update", logx.String("name", name))
	defer func() { uc.end(ctx, logger, "update", started, err, logx.String("name", name)) }()
	if in == nil {
		return nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("skill is required"))
	}
	if err := ValidateSkillName(in.Name); err != nil {
		return nil, err
	}
	if err := uc.requireSkillPermission(ctx, principal, in.Name, "edit"); err != nil {
		return nil, err
	}
	skill := normalizeSkillForUpdate(in)
	out, err = uc.repo.UpdateSkill(ctx, skill)
	if err != nil {
		uc.log.WithContext(ctx).Warn("skill update failed",
			logx.String("name", skill.Name),
			logx.Err(err),
		)
		return nil, err
	}
	uc.log.WithContext(ctx).Info("skill updated",
		logx.String("name", out.Name),
	)
	return out, nil
}

// UpdateSkillVisibility changes a skill between private and public. Requires
// skill.edit because it changes who can read the resource.
func (uc *SkillUsecase) UpdateSkillVisibility(ctx context.Context, principal authn.Principal, name, visibility string) (out *Skill, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "update_visibility", logx.String("name", name), logx.String("visibility", visibility))
	defer func() {
		uc.end(ctx, logger, "update_visibility", started, err, logx.String("name", name), logx.String("visibility", visibility))
	}()
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	visibility = normalizeSkillVisibility(visibility)
	if visibility != SkillVisibilityPrivate && visibility != SkillVisibilityPublic {
		return nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private' or 'public'"))
	}
	if err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {
		return nil, err
	}
	out, err = uc.repo.UpdateSkillVisibility(ctx, name, visibility)
	if err != nil {
		uc.recordAudit(ctx, principal, "skill.visibility.update", auditx.ResultFailure, err.Error(), "skill", name, map[string]any{
			"visibility": visibility,
		})
		uc.log.WithContext(ctx).Warn("skill visibility update failed",
			logx.String("name", name),
			logx.String("visibility", visibility),
			logx.Err(err),
		)
		return nil, err
	}
	uc.recordAudit(ctx, principal, "skill.visibility.update", auditx.ResultSuccess, "", "skill", name, map[string]any{
		"visibility": visibility,
	})
	uc.log.WithContext(ctx).Info("skill visibility updated",
		logx.String("name", out.Name),
		logx.String("visibility", out.Visibility),
		logx.String("updated_by", principal.SubjectID),
	)
	return out, nil
}

// ListSkills lists skills visible to the caller. When authz is enabled,
// uses authz.LookupResources to compute the visible skill set; when
// disabled, returns all skills (dev mode).
func (uc *SkillUsecase) ListSkills(ctx context.Context, principal authn.Principal, opts SkillListOptions) (result *SkillListResult, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "list", logx.Int("limit", opts.Limit), logx.Int("offset", opts.Offset))
	defer func() {
		count := 0
		if result != nil {
			count = len(result.Items)
		}
		uc.end(ctx, logger, "list", started, err, logx.Int("items", count))
	}()
	opts = normalizeSkillListOptions(opts)
	if opts.Offset < 0 {
		return nil, ErrSkillInvalidArgument
	}
	// When authz is enabled, narrow the list to skills the caller can
	// read. When disabled, return all skills (dev mode — production
	// deployments MUST enable authz).
	if uc.authz != nil && principal.IsAuthenticated() {
		visible, err := uc.lookupVisibleSkills(ctx, principal, opts)
		if err != nil {
			// Log + fall back to unfiltered. This is a soft failure — we
			// never want authz outage to make the list endpoint 500.
			uc.log.WithContext(ctx).Warn("authz lookup failed; falling back to unfiltered list",
				logx.String("caller", principal.SubjectID),
				logx.Err(err),
			)
		} else {
			return visible, nil
		}
	}
	return uc.repo.ListSkills(ctx, opts)
}

// GetSkill returns one skill by name. Checks skill.read via authz with
// ownership + public fallbacks so the API stays usable in dev.
func (uc *SkillUsecase) GetSkill(ctx context.Context, principal authn.Principal, name string) (out *Skill, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "get", logx.String("name", name))
	defer func() { uc.end(ctx, logger, "get", started, err, logx.String("name", name)) }()
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	out, err = uc.repo.GetSkill(ctx, name)
	if err != nil {
		return nil, err
	}
	// Authz check with fallbacks. We do NOT call authz.Require here
	// because Require would 403 on denial, but we want ownership /
	// public-skill fallbacks to grant access even when authz denies.
	if !uc.canReadSkill(ctx, principal, out) {
		return nil, ErrSkillPermissionDenied
	}
	return out, nil
}

// DeleteSkill soft-deletes one skill by name. Cascades to versions and
// files; S3 objects are purged best-effort after the DB commit. Requires
// skill.delete permission via authz. After the DB delete succeeds,
// removes all relationships on skill:{name} so authz stops recognizing
// the (now-deleted) resource.
func (uc *SkillUsecase) DeleteSkill(ctx context.Context, principal authn.Principal, name string) (err error) {
	ctx, logger, started := uc.begin(ctx, principal, "delete", logx.String("name", name))
	defer func() { uc.end(ctx, logger, "delete", started, err, logx.String("name", name)) }()
	if err := ValidateSkillName(name); err != nil {
		return err
	}
	if err := uc.requireSkillPermission(ctx, principal, name, "delete"); err != nil {
		return err
	}
	if err := uc.repo.DeleteSkill(ctx, name); err != nil {
		uc.recordAudit(ctx, principal, "skill.delete", auditx.ResultFailure, err.Error(), "skill", name, nil)
		uc.log.WithContext(ctx).Warn("skill delete failed",
			logx.String("name", name),
			logx.Err(err),
		)
		return err
	}
	// Best-effort cleanup of authz relationships. Failure here is logged
	// but NOT returned — the skill row is already soft-deleted, so the
	// orphaned relationships are harmless (they reference a resource that
	// no longer exists in the DB; authz checks will just no-match).
	if uc.authz != nil {
		if err := uc.authz.RevokeResource(ctx, AuthzObjectRef{Type: "skill", ID: name}); err != nil {
			uc.log.WithContext(ctx).Warn("skill authz cleanup failed after delete",
				logx.String("name", name),
				logx.Err(err),
			)
		}
	}
	uc.recordAudit(ctx, principal, "skill.delete", auditx.ResultSuccess, "", "skill", name, nil)
	uc.log.WithContext(ctx).Info("skill deleted", logx.String("name", name))
	return nil
}

// --- SkillVersion ---

// UploadSkillPackage imports a zipped Skill package. The zip must contain
// SKILL.md with YAML front-matter declaring name, description, version.
// If a skill with the parsed name does not exist, one is created with the
// caller as owner. If the target version already exists, the call fails
// unless Overwrite=true.
func (uc *SkillUsecase) UploadSkillPackage(ctx context.Context, principal authn.Principal, in SkillPackageUpload) (out *SkillVersion, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "upload_package", logx.Int("package_bytes", len(in.PackageBytes)), logx.Bool("overwrite", in.Overwrite))
	defer func() {
		name := ""
		version := ""
		if out != nil {
			name = out.SkillName
			version = out.Version
		}
		uc.end(ctx, logger, "upload_package", started, err, logx.String("name", name), logx.String("version", version))
	}()
	// 1. Parse zip.
	parsed, err := skillzipParseSkillFromZip(in.PackageBytes)
	if err != nil {
		uc.log.WithContext(ctx).Warn("skill package parse failed",
			logx.Int("bytes", len(in.PackageBytes)),
			logx.Err(err),
		)
		return nil, errorx.Wrap(err, errorx.Code("SKILL_PACKAGE_INVALID"),
			errorx.WithMessage("invalid skill package"))
	}
	name := normalizeSkillName(parsed.Name)
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	versionName := normalizeSkillVersion(in.TargetVersion)
	if versionName == "" {
		versionName = normalizeSkillVersion(parsed.Version)
	}
	if versionName == "" {
		return nil, ErrSkillInvalidArgument
	}

	// 2. Build the Skill row (upsert if missing).
	existing, getErr := uc.repo.GetSkill(ctx, name)
	if getErr != nil && !isSkillNotFound(getErr) {
		return nil, getErr
	}
	var skill *Skill
	if existing != nil {
		// Authz: require skill.edit for uploads to existing skills.
		// When authz is disabled, fall back to ownership check so dev
		// mode stays usable without SpiceDB.
		if uc.authz != nil {
			if err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {
				return nil, err
			}
		} else if existing.OwnerID != "" && principal.SubjectID != "" && existing.OwnerID != principal.SubjectID {
			uc.log.WithContext(ctx).Warn("skill upload denied: not owner (authz disabled, owner_id fallback)",
				logx.String("name", name),
				logx.String("caller", principal.SubjectID),
				logx.String("owner", existing.OwnerID),
			)
			return nil, errorx.Forbidden(errorx.Code("SKILL_PERMISSION_DENIED"),
				"only the skill owner can upload new versions")
		}
		skill = existing
	} else {
		skill = &Skill{
			Name:        name,
			DisplayName: parsed.Name,
			Description: parsed.Description,
			Version:     versionName,
			Status:      SkillStatusActive,
			Visibility:  SkillVisibilityPrivate,
			OwnerID:     principal.SubjectID,
			OrgID:       principal.OrgID,
			SourceType:  "zip",
			ManifestJSON: mustJSON(map[string]any{
				"name":        parsed.Name,
				"description": parsed.Description,
				"version":     versionName,
			}),
		}
	}

	// 3. Build the SkillVersion row.
	packageBytes := in.PackageBytes
	md5sum := md5Hex(packageBytes)
	sha256sum := sha256Hex(packageBytes)
	version := &SkillVersion{
		SkillName: name,
		Version:   versionName,
		Status:    SkillVersionStatusDraft,
		Author:    principal.SubjectID,
		CommitMsg: in.CommitMsg,
		MD5:       md5sum,
		SHA256:    sha256sum,
		SizeBytes: int64(len(packageBytes)),
		Revision:  sha256sum, // revision = ETag source; we use sha256 for stability
		ManifestJSON: mustJSON(map[string]any{
			"name":        parsed.Name,
			"description": parsed.Description,
			"version":     versionName,
			"metadata":    parsed.Metadata,
		}),
	}

	// 4. Build version files from zip resources. SKILL.md is the entrypoint
	// and must be stored as a normal version file, not just parsed for metadata.
	files := make([]*SkillFile, 0, len(parsed.Resources)+1)
	files = append(files, &SkillFile{
		SkillName: name,
		Version:   versionName,
		Path:      "SKILL.md",
		Name:      "SKILL.md",
		Type:      "text/markdown; charset=utf-8",
		Size:      int64(len([]byte(parsed.SkillMD))),
		Binary:    false,
		Content:   parsed.SkillMD,
	})
	for _, r := range parsed.Resources {
		files = append(files, &SkillFile{
			SkillName: name,
			Version:   versionName,
			Path:      r.Path,
			Name:      r.Name,
			Type:      r.Type,
			Size:      r.Size,
			Binary:    r.Binary,
			Content:   r.Content,
		})
	}

	// 5. Persist + upload to object store (single transaction).
	out, err = uc.repo.SaveSkillPackage(ctx, skill, version, files, packageBytes, in.Overwrite)
	if err != nil {
		uc.log.WithContext(ctx).Warn("skill package save failed",
			logx.String("name", name),
			logx.String("version", versionName),
			logx.Err(err),
		)
		return nil, err
	}
	uc.log.WithContext(ctx).Info("skill package uploaded",
		logx.String("name", name),
		logx.String("version", versionName),
		logx.Int64("size", out.SizeBytes),
		logx.Int("files", len(files)),
	)
	return out, nil
}

// ListSkillVersions lists versions of one skill, oldest first. Requires
// skill.read via authz (with ownership + public fallback).
func (uc *SkillUsecase) ListSkillVersions(ctx context.Context, principal authn.Principal, name string) ([]*SkillVersion, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.ListSkillVersions(ctx, name)
}

// GetSkillVersion returns one version metadata record. Requires skill.read.
func (uc *SkillUsecase) GetSkillVersion(ctx context.Context, principal authn.Principal, name, version string) (*SkillVersion, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if version == "" {
		return nil, ErrSkillInvalidArgument
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.GetSkillVersion(ctx, name, version)
}

// SubmitSkillVersion transitions a version draft → submitted. Requires
// skill.edit.
func (uc *SkillUsecase) SubmitSkillVersion(ctx context.Context, principal authn.Principal, name, version string) (*SkillVersion, error) {
	return uc.transitionVersion(ctx, principal, name, version, SkillVersionStatusDraft, SkillVersionStatusSubmitted, "submit")
}

// PublishSkillVersion transitions a version submitted → published.
// Requires skill.edit.
func (uc *SkillUsecase) PublishSkillVersion(ctx context.Context, principal authn.Principal, name, version string) (*SkillVersion, error) {
	return uc.transitionVersion(ctx, principal, name, version, SkillVersionStatusSubmitted, SkillVersionStatusPublished, "publish")
}

// OnlineSkillVersion transitions a version published → online. Any
// previously-online version of the same skill is automatically demoted
// to published by the repo's CAS update (atomic within the transaction).
// Requires skill.edit.
func (uc *SkillUsecase) OnlineSkillVersion(ctx context.Context, principal authn.Principal, name, version string) (*SkillVersion, error) {
	return uc.transitionVersion(ctx, principal, name, version, SkillVersionStatusPublished, SkillVersionStatusOnline, "online")
}

// OfflineSkillVersion transitions a version online → offline. Requires
// skill.edit.
func (uc *SkillUsecase) OfflineSkillVersion(ctx context.Context, principal authn.Principal, name, version string) (*SkillVersion, error) {
	return uc.transitionVersion(ctx, principal, name, version, SkillVersionStatusOnline, SkillVersionStatusOffline, "offline")
}

func (uc *SkillUsecase) transitionVersion(ctx context.Context, principal authn.Principal, name, version, expected, target, action string) (*SkillVersion, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if version == "" {
		return nil, ErrSkillInvalidArgument
	}
	if err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {
		return nil, err
	}
	out, err := uc.repo.UpdateSkillVersionStatus(ctx, name, version, expected, target)
	if err != nil {
		if isSkillNotFound(err) {
			// CAS failure: either the row does not exist, or its current
			// status is not `expected` (concurrent transition). Surface as
			// a 409 Conflict so the client knows to re-read and retry.
			return nil, errorx.Conflict(errorx.Code("SKILL_VERSION_STATUS_CONFLICT"),
				fmt.Sprintf("version %s is not in %s state", version, expected))
		}
		uc.log.WithContext(ctx).Warn("skill version transition failed",
			logx.String("name", name),
			logx.String("version", version),
			logx.String("action", action),
			logx.Err(err),
		)
		return nil, err
	}
	uc.log.WithContext(ctx).Info("skill version transitioned",
		logx.String("name", name),
		logx.String("version", version),
		logx.String("action", action),
		logx.String("new_status", target),
	)
	return out, nil
}

// DownloadSkillPackage returns the package bytes. If ifNoneMatch equals
// the version's ETag, returns NotModified=true without reading the body.
// Requires skill.read.
func (uc *SkillUsecase) DownloadSkillPackage(ctx context.Context, principal authn.Principal, name, version, ifNoneMatch string) (out *SkillPackageDownload, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "download_package", logx.String("name", name), logx.String("version", version), logx.Bool("has_if_none_match", ifNoneMatch != ""))
	defer func() {
		uc.end(ctx, logger, "download_package", started, err, logx.String("name", name), logx.String("version", version))
	}()
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if version == "" {
		return nil, ErrSkillInvalidArgument
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	out, err = uc.repo.DownloadSkillPackage(ctx, name, version, ifNoneMatch)
	if err != nil {
		return nil, err
	}
	// Best-effort download counter; do not fail the download if this fails.
	if !out.NotModified {
		if err := uc.repo.IncrementSkillVersionDownloadCount(ctx, name, version); err != nil {
			uc.log.WithContext(ctx).Warn("skill download count increment failed",
				logx.String("name", name),
				logx.String("version", version),
				logx.Err(err),
			)
		}
	}
	return out, nil
}

// ListSkillVersionFiles lists files stored for one skill version. Requires
// skill.read.
func (uc *SkillUsecase) ListSkillVersionFiles(ctx context.Context, principal authn.Principal, name, version string) ([]*SkillFile, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if version == "" {
		return nil, ErrSkillInvalidArgument
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.ListSkillVersionFiles(ctx, name, version)
}

// GetSkillVersionFile returns one file by path. Requires skill.read.
func (uc *SkillUsecase) GetSkillVersionFile(ctx context.Context, principal authn.Principal, name, version, filePath string) (*SkillFile, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if version == "" || filePath == "" {
		return nil, ErrSkillInvalidArgument
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	return uc.repo.GetSkillVersionFile(ctx, name, version, filePath)
}

// SkillShare is one authz relationship on a skill resource, surfaced
// for share-management UIs.
type SkillShare struct {
	ResourceType    string
	ResourceID      string
	Relation        string // "viewer" | "editor" | "owner"
	SubjectType     string
	SubjectID       string
	SubjectRelation string
}

// SkillShareInput is the input for CreateSkillShare.
type SkillShareInput struct {
	Name            string
	Relation        string // "viewer" | "editor" (NOT "owner")
	SubjectType     string
	SubjectID       string
	SubjectRelation string
}

// --- Skill share (authz relationship management) ---

// ListSkillShares lists all subjects that have any relation on the named
// skill. Requires skill.read (with ownership + public fallback).
//
// Returns owner / viewer / editor relationships. The owner relation is
// read-only — it can only be set at CreateSkill time and is not
// modifiable via the share RPCs.
func (uc *SkillUsecase) ListSkillShares(ctx context.Context, principal authn.Principal, name string) ([]*SkillShare, error) {
	if err := ValidateSkillName(name); err != nil {
		return nil, err
	}
	if err := uc.requireSkillRead(ctx, principal, name); err != nil {
		return nil, err
	}
	if uc.authz == nil {
		// Dev mode: no relationships to list.
		return []*SkillShare{}, nil
	}
	rels, _, err := uc.authz.ReadRelationships(ctx, AuthzRelationshipFilter{
		ResourceType: "skill",
		ResourceID:   name,
	}, 0, "")
	if err != nil {
		uc.log.WithContext(ctx).Warn("list skill shares failed",
			logx.String("name", name),
			logx.Err(err),
		)
		return nil, err
	}
	out := make([]*SkillShare, 0, len(rels))
	for _, rel := range rels {
		out = append(out, &SkillShare{
			ResourceType:    rel.Resource.Type,
			ResourceID:      rel.Resource.ID,
			Relation:        rel.Relation,
			SubjectType:     rel.Subject.Type,
			SubjectID:       rel.Subject.ID,
			SubjectRelation: rel.Subject.Relation,
		})
	}
	return out, nil
}

// CreateSkillShare grants a viewer or editor relation on the named skill
// to a subject. Requires skill.edit. The relation field accepts only
// "viewer" or "editor"; passing "owner" returns ErrSkillInvalidArgument
// (owner is set at CreateSkill time and is not transferable).
func (uc *SkillUsecase) CreateSkillShare(ctx context.Context, principal authn.Principal, in SkillShareInput) (out *SkillShare, err error) {
	ctx, logger, started := uc.begin(ctx, principal, "create_share", logx.String("name", in.Name), logx.String("subject_type", in.SubjectType))
	defer func() {
		uc.end(ctx, logger, "create_share", started, err, logx.String("name", in.Name), logx.String("subject_type", in.SubjectType))
	}()
	if err := ValidateSkillName(in.Name); err != nil {
		return nil, err
	}
	in.Relation = strings.TrimSpace(in.Relation)
	if in.Relation != "viewer" && in.Relation != "editor" {
		return nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer' or 'editor' (owner is not transferable)"))
	}
	if strings.TrimSpace(in.SubjectType) == "" || strings.TrimSpace(in.SubjectID) == "" {
		return nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("subject_type and subject_id are required"))
	}
	if err := uc.requireSkillPermission(ctx, principal, in.Name, "edit"); err != nil {
		return nil, err
	}
	if uc.authz == nil {
		return nil, ErrSkillPermissionDenied // dev mode: no authz, no share
	}
	if err := uc.authz.GrantRole(ctx,
		AuthzObjectRef{Type: "skill", ID: in.Name},
		in.Relation,
		AuthzSubjectRef{Type: in.SubjectType, ID: in.SubjectID, Relation: in.SubjectRelation},
	); err != nil {
		uc.recordAudit(ctx, principal, "skill.share.create", auditx.ResultFailure, err.Error(), "skill", in.Name, map[string]any{
			"subject_type": in.SubjectType,
			"subject_id":   in.SubjectID,
			"relation":     in.Relation,
		})
		uc.log.WithContext(ctx).Warn("create skill share failed",
			logx.String("name", in.Name),
			logx.String("relation", in.Relation),
			logx.String("subject", in.SubjectType+":"+in.SubjectID),
			logx.Err(err),
		)
		return nil, err
	}
	uc.recordAudit(ctx, principal, "skill.share.create", auditx.ResultSuccess, "", "skill", in.Name, map[string]any{
		"subject_type": in.SubjectType,
		"subject_id":   in.SubjectID,
		"relation":     in.Relation,
	})
	uc.log.WithContext(ctx).Info("skill share created",
		logx.String("name", in.Name),
		logx.String("relation", in.Relation),
		logx.String("subject", in.SubjectType+":"+in.SubjectID),
		logx.String("granted_by", principal.SubjectID),
	)
	return &SkillShare{
		ResourceType:    "skill",
		ResourceID:      in.Name,
		Relation:        in.Relation,
		SubjectType:     in.SubjectType,
		SubjectID:       in.SubjectID,
		SubjectRelation: in.SubjectRelation,
	}, nil
}

// DeleteSkillShare revokes ALL relations between the named skill and the
// named subject (viewer, editor, but NOT owner — owner can only be
// removed by deleting the skill). Requires skill.edit.
func (uc *SkillUsecase) DeleteSkillShare(ctx context.Context, principal authn.Principal, name, subjectType, subjectID string) (err error) {
	ctx, logger, started := uc.begin(ctx, principal, "delete_share", logx.String("name", name), logx.String("subject_type", subjectType))
	defer func() {
		uc.end(ctx, logger, "delete_share", started, err, logx.String("name", name), logx.String("subject_type", subjectType))
	}()
	if err := ValidateSkillName(name); err != nil {
		return err
	}
	if strings.TrimSpace(subjectType) == "" || strings.TrimSpace(subjectID) == "" {
		return errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("subject_type and subject_id are required"))
	}
	if err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {
		return err
	}
	if uc.authz == nil {
		return nil // dev mode: nothing to delete
	}
	// RevokeAll deletes all relations between resource and subject. We
	// then re-grant the owner relation if the subject was the owner, so
	// deleting a share cannot accidentally revoke ownership.
	// Optimization: read current relationships first; if owner is among
	// them, re-grant it after RevokeAll.
	rels, _, _ := uc.authz.ReadRelationships(ctx, AuthzRelationshipFilter{
		ResourceType: "skill",
		ResourceID:   name,
		SubjectType:  subjectType,
		SubjectID:    subjectID,
	}, 0, "")
	if err := uc.authz.RevokeAll(ctx,
		AuthzObjectRef{Type: "skill", ID: name},
		AuthzSubjectRef{Type: subjectType, ID: subjectID},
	); err != nil {
		uc.recordAudit(ctx, principal, "skill.share.delete", auditx.ResultFailure, err.Error(), "skill", name, map[string]any{
			"subject_type": subjectType,
			"subject_id":   subjectID,
		})
		uc.log.WithContext(ctx).Warn("delete skill share failed",
			logx.String("name", name),
			logx.String("subject", subjectType+":"+subjectID),
			logx.Err(err),
		)
		return err
	}
	// Re-grant owner if the subject was the owner (so DeleteSkillShare
	// cannot be used to revoke ownership — that requires DeleteSkill).
	for _, rel := range rels {
		if rel.Relation == "owner" {
			_ = uc.authz.GrantOwner(ctx,
				AuthzObjectRef{Type: "skill", ID: name},
				AuthzSubjectRef{Type: subjectType, ID: subjectID},
			)
			break
		}
	}
	uc.recordAudit(ctx, principal, "skill.share.delete", auditx.ResultSuccess, "", "skill", name, map[string]any{
		"subject_type": subjectType,
		"subject_id":   subjectID,
	})
	uc.log.WithContext(ctx).Info("skill share deleted",
		logx.String("name", name),
		logx.String("subject", subjectType+":"+subjectID),
		logx.String("revoked_by", principal.SubjectID),
	)
	return nil
}

// --- helpers ---

// requireSkillPermission checks that principal has the given permission
// on skill:{name}. When authz is enabled, calls authz.Require (403 on
// denial, no fallback). When authz is disabled, returns nil (allow) so
// dev mode is usable — production MUST enable authz.
//
// Callers MUST NOT use this for read operations; use requireSkillRead
// (with ownership + public fallback) instead.
func (uc *SkillUsecase) requireSkillPermission(ctx context.Context, principal authn.Principal, name, permission string) error {
	if uc.authz == nil {
		// Dev mode: authz disabled. Allow all write operations so the
		// API is usable; production MUST enable authz.
		return nil
	}
	if !principal.IsAuthenticated() {
		return ErrSkillPermissionDenied
	}
	if err := uc.authz.Require(ctx, AuthzCheckRequest{
		Subject:    AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		Resource:   AuthzObjectRef{Type: "skill", ID: name},
		Permission: permission,
		OrgID:      principal.OrgID,
	}); err != nil {
		if errors.Is(err, ErrAuthzPermissionDenied) || errorx.CodeOf(err) == errorx.Code("AUTHZ_PERMISSION_DENIED") {
			return ErrSkillPermissionDenied
		}
		return err
	}
	return nil
}

// requireSkillRead is like requireSkillPermission but with ownership +
// public-visibility fallbacks. Used by GetSkill / ListSkillVersions /
// GetSkillVersion / DownloadSkillPackage / ListSkillVersionFiles /
// GetSkillVersionFile — read operations where a brand-new user with no
// explicit grants should still see public skills and their own skills.
func (uc *SkillUsecase) requireSkillRead(ctx context.Context, principal authn.Principal, name string) error {
	if uc.authz == nil {
		return nil // dev mode
	}
	if !principal.IsAuthenticated() {
		return ErrSkillPermissionDenied
	}
	// Try authz first.
	ok, err := uc.authz.Can(ctx, AuthzCheckRequest{
		Subject:    AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		Resource:   AuthzObjectRef{Type: "skill", ID: name},
		Permission: "read",
		OrgID:      principal.OrgID,
	})
	if err != nil {
		// Backend failure: log + fall through to ownership / public check.
		// We never want authz outage to make read endpoints 500.
		uc.log.WithContext(ctx).Warn("authz check failed; falling back to ownership/public",
			logx.String("name", name),
			logx.String("caller", principal.SubjectID),
			logx.Err(err),
		)
	} else if ok {
		return nil
	}
	// Fallback: load skill row and check ownership / public visibility.
	// This catches the case where the authz owner-grant failed at create
	// time but the user is still the legitimate owner, AND it lets any
	// authenticated user read public skills even without an explicit grant.
	skill, err := uc.repo.GetSkill(ctx, name)
	if err != nil {
		return err
	}
	if !uc.canReadSkill(ctx, principal, skill) {
		return ErrSkillPermissionDenied
	}
	return nil
}

// canReadSkill returns true if principal can read the given skill,
// considering (1) authz allow, (2) ownership fallback, (3) public
// visibility fallback. Used by GetSkill and requireSkillRead.
func (uc *SkillUsecase) canReadSkill(ctx context.Context, principal authn.Principal, skill *Skill) bool {
	if skill == nil {
		return false
	}
	// Public skills are world-readable by any authenticated user.
	if skill.Visibility == SkillVisibilityPublic {
		return true
	}
	// Owner can always read their own skill.
	if principal.IsAuthenticated() && skill.OwnerID != "" && skill.OwnerID == principal.SubjectID {
		return true
	}
	// Authz check (may be a re-check in the requireSkillRead path, but
	// SpiceDB caches internally so the cost is low).
	if uc.authz != nil && principal.IsAuthenticated() {
		ok, _ := uc.authz.Can(ctx, AuthzCheckRequest{
			Subject:    AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
			Resource:   AuthzObjectRef{Type: "skill", ID: skill.Name},
			Permission: "read",
			OrgID:      principal.OrgID,
		})
		if ok {
			return true
		}
	}
	return false
}

// lookupVisibleSkills uses authz.LookupResources to compute the set of
// skill names the caller can read, then loads those skill rows via
// ListSkillsByNames. When authz returns zero visible skills, returns an
// empty result (NOT a fall-through to unfiltered list — the caller
// already does the fall-through on error).
func (uc *SkillUsecase) lookupVisibleSkills(ctx context.Context, principal authn.Principal, opts SkillListOptions) (*SkillListResult, error) {
	result, err := uc.authz.LookupResources(ctx, AuthzLookupResourcesRequest{
		Subject:      AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		ResourceType: "skill",
		Permission:   "read",
		OrgID:        principal.OrgID,
		Limit:        1000, // upper bound; pagination happens in the DB layer
	})
	if err != nil {
		return nil, err
	}
	if len(result.Resources) == 0 {
		return &SkillListResult{Items: []*Skill{}, HasMore: false}, nil
	}
	names := make([]string, 0, len(result.Resources))
	for _, r := range result.Resources {
		names = append(names, r.ID)
	}
	// ListSkillsByNames is not on the current SkillRepo interface; we
	// approximate by calling ListSkills with a name IN ? filter. For
	// simplicity we just call ListSkills and filter client-side — the
	// typical visible-set is small (< 100 skills per user).
	all, err := uc.repo.ListSkills(ctx, opts)
	if err != nil {
		return nil, err
	}
	visible := make([]*Skill, 0, len(all.Items))
	nameSet := make(map[string]struct{}, len(names))
	for _, n := range names {
		nameSet[n] = struct{}{}
	}
	for _, s := range all.Items {
		if _, ok := nameSet[s.Name]; ok {
			visible = append(visible, s)
		}
	}
	return &SkillListResult{
		Items:      visible,
		NextOffset: opts.Offset + len(visible),
		HasMore:    all.HasMore,
	}, nil
}

// firstNonEmptyString returns the first non-empty string in values, or
// "" if all are empty. Used to merge principal.OrgID into skill.OrgID
// without overwriting an explicit value.
func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// recordAudit writes a security audit record for a skill operation.
// Best-effort: failures are logged but never returned to the caller —
// audit is observability, not a data invariant. The audit recorder is
// typically kernel auditx.Noop() in unit tests and a real store
// (memory / postgres / etc.) in production.
//
// Call this from:
//   - CreateSkill (success / failure)
//   - UpdateSkill (success / failure)
//   - DeleteSkill (success / failure)
//   - UploadSkillPackage (success / failure)
//   - Submit/Publish/Online/OfflineSkillVersion (success / failure)
//   - CreateSkillShare / DeleteSkillShare (success / failure)
//
// Read operations (Get / List) do NOT need audit — they are too high-
// volume and access logs (kernel logx access_log) already capture them.
func (uc *SkillUsecase) recordAudit(ctx context.Context, principal authn.Principal, action, result, reason string, resourceType, resourceID string, extra map[string]any) {
	if uc.audit == nil {
		return
	}
	rec := auditx.Record{
		Time:     time.Now(),
		Action:   action,
		Result:   result,
		Severity: severityForResult(result),
		Reason:   reason,
		Actor:    auditx.Actor{SubjectID: principal.SubjectID, SubjectType: principal.SubjectType, OrgID: principal.OrgID, Name: principal.Name, Email: principal.Email},
		Resource: auditx.Resource{Type: resourceType, ID: resourceID, OrgID: principal.OrgID},
		Metadata: auditx.AttributeSet(extra),
	}
	if err := uc.audit.Record(ctx, rec); err != nil {
		uc.log.WithContext(ctx).Warn("audit record failed",
			logx.String("action", action),
			logx.String("resource", resourceType+":"+resourceID),
			logx.Err(err),
		)
	}
}

// severityForResult maps an audit result string to a severity level.
// "success" → info, "denied" → warning (potential abuse signal),
// "failure" → warning (operational issue).
func severityForResult(result string) string {
	switch result {
	case auditx.ResultSuccess:
		return auditx.SeverityInfo
	case auditx.ResultDenied:
		return auditx.SeverityWarning
	case auditx.ResultFailure:
		return auditx.SeverityWarning
	default:
		return auditx.SeverityInfo
	}
}

// ValidateSkillName returns ErrSkillInvalidArgument if name is empty or
// does not match the canonical Skill name regex.
func ValidateSkillName(name string) error {
	if !skillNameRE.MatchString(name) {
		return errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("name must match "+skillNameRE.String()))
	}
	return nil
}

func normalizeSkillName(name string) string {
	return strings.TrimSpace(name)
}

func normalizeSkillVersion(v string) string {
	return strings.TrimSpace(v)
}

func normalizeSkillVisibility(v string) string {
	return strings.ToLower(strings.TrimSpace(v))
}

func normalizeSkillForCreate(in *Skill) *Skill {
	out := *in
	out.Name = normalizeSkillName(out.Name)
	if out.Status == "" {
		out.Status = SkillStatusActive
	}
	out.Visibility = normalizeSkillVisibility(out.Visibility)
	if out.Visibility == "" {
		out.Visibility = SkillVisibilityPrivate
	}
	if out.Version == "" {
		out.Version = DefaultSkillVersion
	}
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if strings.TrimSpace(out.ManifestJSON) == "" {
		out.ManifestJSON = "{}"
	}
	return &out
}

// normalizeSkillForUpdate zeroes out fields the UpdateSkill RPC is not
// allowed to touch (owner_id, visibility, status). The data layer also
// omits these from the UPDATE map as defense in depth.
func normalizeSkillForUpdate(in *Skill) *Skill {
	out := *in
	out.Name = normalizeSkillName(out.Name)
	// owner_id / visibility / status intentionally not copied.
	out.Tags = append([]string(nil), in.Tags...)
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if strings.TrimSpace(out.ManifestJSON) == "" {
		out.ManifestJSON = "{}"
	}
	return &out
}

func normalizeSkillListOptions(opts SkillListOptions) SkillListOptions {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 100 {
		opts.Limit = 100
	}
	opts.Query = strings.TrimSpace(opts.Query)
	opts.Status = strings.TrimSpace(opts.Status)
	opts.Visibility = strings.TrimSpace(opts.Visibility)
	return opts
}

// isSkillNotFound returns true if err is one of the *NotFound sentinels
// defined in this package. Used by UploadSkillPackage to distinguish
// "skill does not exist yet" (expected) from real DB errors.
func isSkillNotFound(err error) bool {
	switch {
	case err == nil:
		return false
	case err == ErrSkillNotFound, err == ErrSkillVersionNotFound, err == ErrSkillFileNotFound:
		return true
	}
	// Also handle wrapped errors via errorx.As.
	if e, ok := errorx.As(err); ok {
		return e.Code() == errorx.Code("SKILL_NOT_FOUND") ||
			e.Code() == errorx.Code("SKILL_VERSION_NOT_FOUND") ||
			e.Code() == errorx.Code("SKILL_FILE_NOT_FOUND")
	}
	return false
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// EncodePageToken encodes an offset as an opaque page token. We use the
// decimal string representation — simple and debuggable. Switch to a
// signed/encrypted token if pagination security becomes a concern.
func EncodePageToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	return strconv.Itoa(offset)
}

// DecodePageToken decodes a page token from EncodePageToken. Returns 0
// for empty token, ErrSkillInvalidArgument for malformed token.
func DecodePageToken(token string) (int, error) {
	if strings.TrimSpace(token) == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(token)
	if err != nil || offset < 0 {
		return 0, ErrSkillInvalidArgument
	}
	return offset, nil
}

// --- skillzip bridge ---
//
// We import internal/skillzip only through a thin wrapper so biz tests
// can stub the zip parser without pulling archive/zip into test deps.
// The wrapper is a package-level var so tests can replace it.

var skillzipParseSkillFromZip = func(b []byte) (*skillzipSkill, error) {
	parsed, err := skillzipParse(b)
	if err != nil {
		return nil, err
	}
	return &skillzipSkill{
		Name:        parsed.Name,
		Description: parsed.Description,
		Version:     parsed.Version,
		SkillMD:     parsed.SkillMD,
		Metadata:    parsed.Metadata,
		Resources:   convertResources(parsed.Resources),
	}, nil
}

// skillzipSkill is the biz-side view of a parsed skill zip, decoupled
// from internal/skillzip.Skill so biz does not import that package
// directly (the import is hidden in skillzipParse below).
type skillzipSkill struct {
	Name        string
	Description string
	Version     string
	SkillMD     string
	Metadata    map[string]string
	Resources   []skillzipResource
}

type skillzipResource struct {
	Path    string
	Name    string
	Type    string
	Content string
	Size    int64
	Binary  bool
}

// skillzipParse is implemented in skillzip_bridge.go to keep this file
// free of the internal/skillzip import. The bridge file is the only
// place biz imports internal/skillzip.
var skillzipParse = func(b []byte) (*skillzipBridgeResult, error) {
	return nil, fmt.Errorf("skillzipParse not wired (see skillzip_bridge.go)")
}

type skillzipBridgeResult struct {
	Name        string
	Description string
	Version     string
	SkillMD     string
	Metadata    map[string]string
	Resources   []skillzipBridgeResource
}

type skillzipBridgeResource struct {
	Path    string
	Name    string
	Type    string
	Content string
	Size    int64
	Binary  bool
}

func convertResources(in []skillzipBridgeResource) []skillzipResource {
	out := make([]skillzipResource, 0, len(in))
	for _, r := range in {
		out = append(out, skillzipResource(r))
	}
	// Stable order so file rows are deterministic.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// base64DecodeOrEmpty returns the decoded bytes of a base64-encoded
// string, or nil if the input is empty. Used by tests; kept here so
// service tests can reuse the same decoding logic.
func base64DecodeOrEmpty(s string) []byte {
	if s == "" {
		return nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}
