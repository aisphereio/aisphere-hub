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

	SkillShareRelationViewer = "viewer"
	SkillShareRelationEditor = "editor"
)

// NormalizeRootSkillCreate stamps server-owned placement fields for the
// root Skill catalog model.
//
// Hub does not create Skills under a Hub organization/group/project. The
// authenticated principal becomes the owner and org/project placement is not
// accepted from client payloads. Keeping this as a small helper makes the
// product rule reusable by service code, upload/import code, and tests.
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

// NormalizeSkillShareRelation accepts only the two grantable relations for
// user-managed Skill sharing. Ownership is intentionally not transferable from
// the share dialog.
func NormalizeSkillShareRelation(relation string) (string, error) {
	rel := strings.ToLower(strings.TrimSpace(relation))
	if rel == SkillShareRelationViewer || rel == SkillShareRelationEditor {
		return rel, nil
	}
	return "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer' or 'editor'"))
}
