package biz

import (
	"context"

	"github.com/aisphereio/kernel/authn"
)

// CreateRootSkill creates a Skill in the global Hub root catalog.
//
// This is the public API-facing variant of CreateSkill. It intentionally
// ignores client supplied owner_id/org_id/project_id and stamps ownership from
// the authenticated principal. Hub does not support placing a Skill under a Hub
// org/group/project; IAM/Casdoor groups are authorization subjects only.
func (uc *SkillUsecase) CreateRootSkill(ctx context.Context, principal authn.Principal, in *Skill) (*Skill, error) {
	return uc.CreateSkill(ctx, principal, NormalizeRootSkillCreate(in, principal))
}

// ListRootCatalogSkills lists the Skill root catalog visible to the caller.
//
// Unlike the older ListSkills path, this method does not rely solely on
// authz.LookupResources. Public Skills are stored as row visibility, not as a
// SpiceDB relationship, so the API must always apply the row-level
// public/owner fallback when building the visible list.
func (uc *SkillUsecase) ListRootCatalogSkills(ctx context.Context, principal authn.Principal, opts SkillListOptions) (*SkillListResult, error) {
	opts = normalizeSkillListOptions(opts)
	all, err := uc.repo.ListSkills(ctx, opts)
	if err != nil {
		return nil, err
	}
	if uc.authz == nil {
		return all, nil
	}
	items := make([]*Skill, 0, len(all.Items))
	for _, item := range all.Items {
		if uc.canReadSkill(ctx, principal, item) {
			items = append(items, item)
		}
	}
	return &SkillListResult{
		Items:      items,
		NextOffset: all.NextOffset,
		HasMore:    all.HasMore,
	}, nil
}

// CreateRootSkillShare creates a Skill share using the IAM/Authz subject model.
//
// The UI may select a user or a Casdoor/IAM group. Hub does not expand or own
// group membership; group sharing is normalized to group:{id}#member and then
// delegated to the existing Skill share implementation.
func (uc *SkillUsecase) CreateRootSkillShare(ctx context.Context, principal authn.Principal, in SkillShareInput) (*SkillShare, error) {
	relation, err := NormalizeSkillShareRelation(in.Relation)
	if err != nil {
		return nil, err
	}
	subject, err := NormalizeSkillShareSubject(in.SubjectType, in.SubjectID, in.SubjectRelation)
	if err != nil {
		return nil, err
	}
	return uc.CreateSkillShare(ctx, principal, SkillShareInput{
		Name:            in.Name,
		Relation:        relation,
		SubjectType:     subject.Type,
		SubjectID:       subject.ID,
		SubjectRelation: subject.Relation,
	})
}
