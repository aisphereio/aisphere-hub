package biz

import (
	"context"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
	"github.com/aisphereio/kernel/logx"
)

const (
	SkillSetVisibilityPrivate  = "private"
	SkillSetVisibilityInternal = "internal"
	SkillSetVisibilityPublic   = "public"
)

var (
	ErrSkillSetNotFound = errorx.NotFound(
		errorx.Code("SKILLSET_NOT_FOUND"),
		"skill set not found",
	)
	ErrSkillSetAlreadyExists = errorx.Conflict(
		errorx.Code("SKILLSET_ALREADY_EXISTS"),
		"skill set already exists",
	)
	ErrSkillSetInvalidArgument = errorx.BadRequest(
		errorx.Code("SKILLSET_INVALID_ARGUMENT"),
		"invalid skill set argument",
	)
	ErrSkillSetSkillNotFound = errorx.BadRequest(
		errorx.Code("SKILLSET_SKILL_NOT_FOUND"),
		"skill set contains a skill that does not exist",
	)
	ErrSkillSetPermissionDenied = errorx.Forbidden(
		errorx.Code("SKILLSET_PERMISSION_DENIED"),
		"permission denied",
	)

	skillSetNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

// SkillSet is a lightweight, ordered collection of Skill references.
// It deliberately owns no package, runtime, release or Skill version state.
type SkillSet struct {
	ID          int64
	Name        string
	DisplayName string
	Description string
	Visibility  string
	OwnerID     string
	OrgID       string
	Labels      map[string]string
	Members     []SkillSetMember
	CreateTime  time.Time
	UpdateTime  time.Time
}

// SkillSetMember only records membership and display order. The referenced
// Skill keeps its own release and lifecycle state.
type SkillSetMember struct {
	SkillName string
	Order     int
}

type SkillSetPatch struct {
	DisplayName *string
	Description *string
	Visibility  *string
	Labels      *map[string]string
	Members     *[]SkillSetMember
}

type SkillSetListOptions struct {
	Limit        int
	Offset       int
	Query        string
	OwnerID      string
	OrgID        string
	VisibleNames []string
}

type SkillSetListResult struct {
	Items []*SkillSet
	Total int64
}

type SkillSetRepo interface {
	CreateSkillSet(ctx context.Context, set *SkillSet) (*SkillSet, error)
	UpdateSkillSet(ctx context.Context, name string, patch SkillSetPatch) (*SkillSet, error)
	ListSkillSets(ctx context.Context, opts SkillSetListOptions) (*SkillSetListResult, error)
	GetSkillSet(ctx context.Context, name string) (*SkillSet, error)
	DeleteSkillSet(ctx context.Context, name string) error
	ListSkillSetNamesBySkill(ctx context.Context, skillName string) ([]string, error)
}

type SkillSetUsecase struct {
	repo  SkillSetRepo
	authz *AuthzUsecase
	log   logx.Logger
}

func NewSkillSetUsecase(repo SkillSetRepo, authz *AuthzUsecase, logger logx.Logger) *SkillSetUsecase {
	if logger == nil {
		logger = logx.Noop()
	}
	return &SkillSetUsecase{repo: repo, authz: authz, log: logger.Named("skillset")}
}

func (uc *SkillSetUsecase) Create(ctx context.Context, principal authn.Principal, in *SkillSet) (*SkillSet, error) {
	if !principal.IsAuthenticated() {
		return nil, ErrSkillSetPermissionDenied
	}
	if in == nil {
		return nil, errorx.From(ErrSkillSetInvalidArgument, errorx.WithMessage("skill set is required"))
	}
	if err := ValidateSkillSetName(in.Name); err != nil {
		return nil, err
	}
	set := *in
	set.Name = strings.TrimSpace(set.Name)
	set.DisplayName = strings.TrimSpace(set.DisplayName)
	set.Description = strings.TrimSpace(set.Description)
	set.Visibility = normalizeSkillSetVisibility(set.Visibility)
	set.OwnerID = principal.SubjectID
	set.OrgID = principal.OrgID
	set.Labels = cloneStringMap(set.Labels)
	set.Members = normalizeSkillSetMembers(set.Members)

	out, err := uc.repo.CreateSkillSet(ctx, &set)
	if err != nil {
		return nil, err
	}
	if uc.authz != nil {
		if err := uc.authz.GrantOwner(ctx,
			AuthzObjectRef{Type: "skillset", ID: out.Name},
			AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		); err != nil {
			uc.log.WithContext(ctx).Warn("skill set owner grant failed; durable owner_id remains authoritative",
				logx.String("name", out.Name), logx.String("owner", principal.SubjectID), logx.Err(err))
		}
	}
	return out, nil
}

func (uc *SkillSetUsecase) Update(ctx context.Context, principal authn.Principal, name string, patch SkillSetPatch) (*SkillSet, error) {
	if err := ValidateSkillSetName(name); err != nil {
		return nil, err
	}
	current, err := uc.repo.GetSkillSet(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := uc.require(ctx, principal, current, "edit"); err != nil {
		return nil, err
	}
	if patch.DisplayName != nil {
		v := strings.TrimSpace(*patch.DisplayName)
		patch.DisplayName = &v
	}
	if patch.Description != nil {
		v := strings.TrimSpace(*patch.Description)
		patch.Description = &v
	}
	if patch.Visibility != nil {
		v := normalizeSkillSetVisibility(*patch.Visibility)
		patch.Visibility = &v
	}
	if patch.Labels != nil {
		v := cloneStringMap(*patch.Labels)
		patch.Labels = &v
	}
	if patch.Members != nil {
		v := normalizeSkillSetMembers(*patch.Members)
		patch.Members = &v
	}
	return uc.repo.UpdateSkillSet(ctx, name, patch)
}

func (uc *SkillSetUsecase) Get(ctx context.Context, principal authn.Principal, name string) (*SkillSet, error) {
	if err := ValidateSkillSetName(name); err != nil {
		return nil, err
	}
	set, err := uc.repo.GetSkillSet(ctx, name)
	if err != nil {
		return nil, err
	}
	if uc.canRead(ctx, principal, set) {
		return set, nil
	}
	return nil, ErrSkillSetPermissionDenied
}

func (uc *SkillSetUsecase) List(ctx context.Context, principal authn.Principal, opts SkillSetListOptions) (*SkillSetListResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 20
	}
	if opts.Limit > 200 {
		opts.Limit = 200
	}
	if opts.Offset < 0 {
		return nil, ErrSkillSetInvalidArgument
	}
	if principal.IsAuthenticated() {
		opts.OwnerID = principal.SubjectID
		opts.OrgID = principal.OrgID
		if uc.authz != nil {
			lookup, err := uc.authz.LookupResources(ctx, AuthzLookupResourcesRequest{
				Subject:      AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
				ResourceType: "skillset",
				Permission:   "read",
				OrgID:        principal.OrgID,
				Limit:        1000,
			})
			if err == nil {
				for _, ref := range lookup.Resources {
					opts.VisibleNames = append(opts.VisibleNames, ref.ID)
				}
			}
		}
	}
	return uc.repo.ListSkillSets(ctx, opts)
}

func (uc *SkillSetUsecase) Delete(ctx context.Context, principal authn.Principal, name string) error {
	if err := ValidateSkillSetName(name); err != nil {
		return err
	}
	current, err := uc.repo.GetSkillSet(ctx, name)
	if err != nil {
		return err
	}
	if err := uc.require(ctx, principal, current, "delete"); err != nil {
		return err
	}
	if err := uc.repo.DeleteSkillSet(ctx, name); err != nil {
		return err
	}
	if uc.authz != nil {
		if err := uc.authz.RevokeResource(ctx, AuthzObjectRef{Type: "skillset", ID: name}); err != nil {
			uc.log.WithContext(ctx).Warn("skill set relationship cleanup failed", logx.String("name", name), logx.Err(err))
		}
	}
	return nil
}

func (uc *SkillSetUsecase) ListNamesBySkill(ctx context.Context, principal authn.Principal, skillName string) ([]string, error) {
	if err := ValidateSkillName(skillName); err != nil {
		return nil, err
	}
	names, err := uc.repo.ListSkillSetNamesBySkill(ctx, skillName)
	if err != nil {
		return nil, err
	}
	visible := make([]string, 0, len(names))
	for _, name := range names {
		set, getErr := uc.repo.GetSkillSet(ctx, name)
		if getErr == nil && uc.canRead(ctx, principal, set) {
			visible = append(visible, name)
		}
	}
	return visible, nil
}

func (uc *SkillSetUsecase) canRead(ctx context.Context, principal authn.Principal, set *SkillSet) bool {
	if set == nil {
		return false
	}
	if set.Visibility == SkillSetVisibilityPublic {
		return true
	}
	if principal.IsAuthenticated() && principal.SubjectID == set.OwnerID {
		return true
	}
	if principal.IsAuthenticated() && set.Visibility == SkillSetVisibilityInternal && set.OrgID != "" && set.OrgID == principal.OrgID {
		return true
	}
	if uc.authz != nil && principal.IsAuthenticated() {
		allowed, err := uc.authz.Can(ctx, AuthzCheckRequest{
			Subject:    AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
			Resource:   AuthzObjectRef{Type: "skillset", ID: set.Name},
			Permission: "read",
			OrgID:      principal.OrgID,
		})
		return err == nil && allowed
	}
	return false
}

func (uc *SkillSetUsecase) require(ctx context.Context, principal authn.Principal, set *SkillSet, permission string) error {
	if !principal.IsAuthenticated() || set == nil {
		return ErrSkillSetPermissionDenied
	}
	if principal.SubjectID == set.OwnerID {
		return nil
	}
	if uc.authz == nil {
		return ErrSkillSetPermissionDenied
	}
	if err := uc.authz.Require(ctx, AuthzCheckRequest{
		Subject:    AuthzSubjectRef{Type: "user", ID: principal.SubjectID},
		Resource:   AuthzObjectRef{Type: "skillset", ID: set.Name},
		Permission: permission,
		OrgID:      principal.OrgID,
	}); err != nil {
		return ErrSkillSetPermissionDenied
	}
	return nil
}

func ValidateSkillSetName(name string) error {
	name = strings.TrimSpace(name)
	if !skillSetNameRE.MatchString(name) {
		return errorx.From(ErrSkillSetInvalidArgument, errorx.WithMessage("skill set name must match [A-Za-z0-9][A-Za-z0-9_.-]{0,127}"))
	}
	return nil
}

func normalizeSkillSetVisibility(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case SkillSetVisibilityInternal:
		return SkillSetVisibilityInternal
	case SkillSetVisibilityPublic:
		return SkillSetVisibilityPublic
	default:
		return SkillSetVisibilityPrivate
	}
}

func normalizeSkillSetMembers(in []SkillSetMember) []SkillSetMember {
	seen := make(map[string]struct{}, len(in))
	out := make([]SkillSetMember, 0, len(in))
	for _, member := range in {
		name := strings.TrimSpace(member.SkillName)
		if name == "" || !skillNameRE.MatchString(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, SkillSetMember{SkillName: name, Order: member.Order})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Order == out[j].Order {
			return out[i].SkillName < out[j].SkillName
		}
		return out[i].Order < out[j].Order
	})
	for i := range out {
		out[i].Order = i
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		key := strings.TrimSpace(k)
		if key != "" {
			out[key] = strings.TrimSpace(v)
		}
	}
	return out
}
