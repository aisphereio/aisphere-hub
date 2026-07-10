package biz

import (
	"context"

	"github.com/aisphereio/kernel/authn"
)

const rootSkillCatalogScanBatchSize = 100

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
// Visibility is evaluated before the API page is finalized. Public Skills are
// stored as row visibility rather than as SpiceDB relationships, so filtering a
// single database page after pagination can return a sparse or empty page even
// when later visible Skills exist. This method scans database batches until it
// fills the requested visible page or reaches the end of the catalog.
func (uc *SkillUsecase) ListRootCatalogSkills(ctx context.Context, principal authn.Principal, opts SkillListOptions) (*SkillListResult, error) {
	opts = normalizeSkillListOptions(opts)
	if uc.authz == nil {
		return uc.repo.ListSkills(ctx, opts)
	}

	requestedLimit := opts.Limit
	scanOffset := opts.Offset
	visible := make([]*Skill, 0, requestedLimit)
	hasMoreSource := true

	for len(visible) < requestedLimit && hasMoreSource {
		batchLimit := rootSkillCatalogScanBatchSize
		if batchLimit < requestedLimit {
			batchLimit = requestedLimit
		}
		batchOpts := opts
		batchOpts.Limit = batchLimit
		batchOpts.Offset = scanOffset

		batch, err := uc.repo.ListSkills(ctx, batchOpts)
		if err != nil {
			return nil, err
		}
		if batch == nil || len(batch.Items) == 0 {
			hasMoreSource = false
			break
		}

		for _, item := range batch.Items {
			scanOffset++
			if uc.canReadSkill(ctx, principal, item) {
				visible = append(visible, item)
				if len(visible) == requestedLimit {
					break
				}
			}
		}

		hasMoreSource = batch.HasMore
		if len(batch.Items) < batchLimit {
			hasMoreSource = false
		}
	}

	return &SkillListResult{
		Items:      visible,
		NextOffset: scanOffset,
		HasMore:    hasMoreSource,
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
