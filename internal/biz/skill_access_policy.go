package biz

import (
	"strings"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

const (
	SkillSubjectTypeUser    = "user"
	SkillSubjectTypeGroup   = "group"
	SkillSubjectTypeService = "service"

	SkillSubjectRelationMember = "member"

	SkillShareRelationViewer   = "viewer"
	SkillShareRelationEditor   = "editor"
	SkillShareRelationReviewer = "reviewer"

	// SkillVisibilityInternal means every authenticated principal whose
	// stable IAM org_id matches Skill.OrgID can discover and read the Skill.
	// The Skill still lives in the global root catalog; OrgID is a governance
	// and access boundary, not a catalog parent.
	SkillVisibilityInternal = "internal"
)

// NormalizeRootSkillCreate stamps server-owned placement fields for the
// root Skill catalog model.
//
// Hub does not create Skills under a Hub organization/group/project. The
// authenticated principal becomes the owner; principal.OrgID is retained only
// as the governing organization used by internal visibility, IAM directory
// pickers, audit, and future quota policy. Project placement is never accepted
// from client payloads. Keeping this as a small helper makes the product rule
// reusable by service code, upload/import code, and tests.
func NormalizeRootSkillCreate(skill *Skill, principal authn.Principal) *Skill {
	if skill == nil {
		return nil
	}
	out := *skill
	out.OwnerID = ""
	out.OrgID = ""
	out.ProjectID = ""
	if principal.IsAuthenticated() {
		out.OwnerID = principal.SubjectID
		out.OrgID = principal.OrgID
	}
	return &out
}

// NormalizeSkillShareSubject converts the UI/IAM principal picker result into
// the exact SpiceDB subject representation used by Skill sharing.
//
// Hub never expands group membership locally. IAM/Casdoor membership is already
// projected to SpiceDB, so a group share must target group:{id}#member.
func NormalizeSkillShareSubject(subjectType, subjectID, subjectRelation string) (AuthzSubjectRef, error) {
	t := strings.ToLower(strings.TrimSpace(subjectType))
	id := strings.TrimSpace(subjectID)
	rel := strings.TrimSpace(subjectRelation)
	if t == "" || id == "" {
		return AuthzSubjectRef{}, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("subject_type and subject_id are required"))
	}
	switch t {
	case SkillSubjectTypeUser:
		return AuthzSubjectRef{Type: t, ID: id}, nil
	case SkillSubjectTypeGroup:
		if rel == "" {
			rel = SkillSubjectRelationMember
		}
		if rel != SkillSubjectRelationMember {
			return AuthzSubjectRef{}, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("group subject_relation must be 'member'"))
		}
		return AuthzSubjectRef{Type: t, ID: id, Relation: rel}, nil
	case SkillSubjectTypeService:
		return AuthzSubjectRef{Type: t, ID: id}, nil
	default:
		return AuthzSubjectRef{}, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("subject_type must be 'user', 'group', or 'service'"))
	}
}

// NormalizeSkillShareRelation accepts the grantable relations defined by
// the IAM-owned SpiceDB skill model. Ownership is intentionally not transferable
// from the share dialog. reviewer is the publish/review role; editor cannot
// change visibility or grant other users access.
func NormalizeSkillShareRelation(relation string) (string, error) {
	rel := strings.ToLower(strings.TrimSpace(relation))
	switch rel {
	case SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer:
		return rel, nil
	default:
		return "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer', 'editor', or 'reviewer'"))
	}
}

// NormalizeSkillVisibility validates the three product visibility states.
func NormalizeSkillVisibility(visibility string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(visibility))
	switch v {
	case SkillVisibilityPrivate, SkillVisibilityInternal, SkillVisibilityPublic:
		return v, nil
	default:
		return "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private', 'internal', or 'public'"))
	}
}

// CanReadSkillByImplicitPolicy evaluates the durable Hub-side fallbacks used
// when IAM/SpiceDB has no explicit relation or is temporarily unavailable.
// It never grants write, publish, share, visibility, or delete permissions.
func CanReadSkillByImplicitPolicy(principal authn.Principal, skill *Skill) bool {
	if skill == nil || !principal.IsAuthenticated() {
		return false
	}
	if skill.OwnerID != "" && skill.OwnerID == principal.SubjectID {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(skill.Visibility)) {
	case SkillVisibilityPublic:
		// Current /v1/skills routes are authenticated, so public means every
		// platform user. A future anonymous catalog must use separate PUBLIC
		// routes that expose only online versions.
		return true
	case SkillVisibilityInternal:
		return skill.OrgID != "" && principal.OrgID != "" && skill.OrgID == principal.OrgID
	default:
		return false
	}
}
