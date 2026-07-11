from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text(encoding="utf-8")
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one match, found {count}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1), encoding="utf-8")


replace_once(
    "internal/biz/skill_access_policy.go",
    '''func NormalizeSkillVisibility(visibility string) (string, error) {\n\tv := strings.ToLower(strings.TrimSpace(visibility))\n\tswitch v {\n\tcase SkillVisibilityPrivate, SkillVisibilityInternal, SkillVisibilityPublic:\n\t\treturn v, nil\n\tdefault:\n\t\treturn "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private', 'internal', or 'public'"))\n\t}\n}\n''',
    '''func NormalizeSkillVisibility(visibility string) (string, error) {\n\tv := strings.ToLower(strings.TrimSpace(visibility))\n\tswitch v {\n\tcase SkillVisibilityPrivate, SkillVisibilityInternal, SkillVisibilityPublic:\n\t\treturn v, nil\n\tdefault:\n\t\treturn "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private', 'internal', or 'public'"))\n\t}\n}\n\n// SkillShareUnderlyingRelations translates one product role into the IAM-owned\n// SpiceDB relations needed to make it usable. The current shared Schema keeps\n// reviewer separate from view, so the reviewer product role is represented by\n// reviewer + viewer.\nfunc SkillShareUnderlyingRelations(role string) []string {\n\tswitch role {\n\tcase SkillShareRelationReviewer:\n\t\treturn []string{SkillShareRelationReviewer, SkillShareRelationViewer}\n\tcase SkillShareRelationEditor:\n\t\treturn []string{SkillShareRelationEditor}\n\tdefault:\n\t\treturn []string{SkillShareRelationViewer}\n\t}\n}\n\n// CollapseSkillShareRelationships converts the low-level relation set into one\n// product role per subject. Role precedence is owner > reviewer > editor > viewer.\nfunc CollapseSkillShareRelationships(rels []AuthzRelationship) []*SkillShare {\n\ttype entry struct {\n\t\tshare    *SkillShare\n\t\tpriority int\n\t}\n\tbySubject := make(map[string]entry, len(rels))\n\torder := make([]string, 0, len(rels))\n\tpriority := map[string]int{\n\t\t"owner":                    4,\n\t\tSkillShareRelationReviewer: 3,\n\t\tSkillShareRelationEditor:   2,\n\t\tSkillShareRelationViewer:   1,\n\t}\n\tfor _, rel := range rels {\n\t\tp, ok := priority[rel.Relation]\n\t\tif !ok {\n\t\t\tcontinue\n\t\t}\n\t\tkey := rel.Subject.Type + ":" + rel.Subject.ID + "#" + rel.Subject.Relation\n\t\tcurrent, exists := bySubject[key]\n\t\tif exists && current.priority >= p {\n\t\t\tcontinue\n\t\t}\n\t\tif !exists {\n\t\t\torder = append(order, key)\n\t\t}\n\t\tbySubject[key] = entry{\n\t\t\tpriority: p,\n\t\t\tshare: &SkillShare{\n\t\t\t\tResourceType:    rel.Resource.Type,\n\t\t\t\tResourceID:      rel.Resource.ID,\n\t\t\t\tRelation:        rel.Relation,\n\t\t\t\tSubjectType:     rel.Subject.Type,\n\t\t\t\tSubjectID:       rel.Subject.ID,\n\t\t\t\tSubjectRelation: rel.Subject.Relation,\n\t\t\t},\n\t\t}\n\t}\n\tout := make([]*SkillShare, 0, len(order))\n\tfor _, key := range order {\n\t\tout = append(out, bySubject[key].share)\n\t}\n\treturn out\n}\n''',
)

replace_once(
    "internal/biz/skill.go",
    '''// ListSkillShares lists all subjects that have any relation on the named\n// skill. Requires skill.read (with ownership + public fallback).''',
    '''// ListSkillShares lists all subjects that have any relation on the named\n// skill. Requires skill.manage because the subject list is authorization metadata.''',
)
replace_once(
    "internal/biz/skill.go",
    '''func (uc *SkillUsecase) ListSkillShares(ctx context.Context, principal authn.Principal, name string) ([]*SkillShare, error) {\n\tif err := ValidateSkillName(name); err != nil {\n\t\treturn nil, err\n\t}\n\tif err := uc.requireSkillRead(ctx, principal, name); err != nil {''',
    '''func (uc *SkillUsecase) ListSkillShares(ctx context.Context, principal authn.Principal, name string) ([]*SkillShare, error) {\n\tif err := ValidateSkillName(name); err != nil {\n\t\treturn nil, err\n\t}\n\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tout := make([]*SkillShare, 0, len(rels))\n\tfor _, rel := range rels {\n\t\tout = append(out, &SkillShare{\n\t\t\tResourceType:    rel.Resource.Type,\n\t\t\tResourceID:      rel.Resource.ID,\n\t\t\tRelation:        rel.Relation,\n\t\t\tSubjectType:     rel.Subject.Type,\n\t\t\tSubjectID:       rel.Subject.ID,\n\t\t\tSubjectRelation: rel.Subject.Relation,\n\t\t})\n\t}\n\treturn out, nil''',
    '''\treturn CollapseSkillShareRelationships(rels), nil''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tif err := uc.authz.GrantRole(ctx,\n\t\tAuthzObjectRef{Type: "skill", ID: in.Name},\n\t\tin.Relation,\n\t\tAuthzSubjectRef{Type: in.SubjectType, ID: in.SubjectID, Relation: in.SubjectRelation},\n\t); err != nil {''',
    '''\tsubject := AuthzSubjectRef{Type: in.SubjectType, ID: in.SubjectID, Relation: in.SubjectRelation}\n\tresource := AuthzObjectRef{Type: "skill", ID: in.Name}\n\texisting, _, err := uc.authz.ReadRelationships(ctx, AuthzRelationshipFilter{\n\t\tResourceType: resource.Type,\n\t\tResourceID:   resource.ID,\n\t\tSubjectType:  subject.Type,\n\t\tSubjectID:    subject.ID,\n\t}, 0, "")\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tif err := uc.authz.RevokeAll(ctx, resource, subject); err != nil {\n\t\treturn nil, err\n\t}\n\tnewRelationships := make([]AuthzRelationship, 0, 3)\n\tfor _, rel := range existing {\n\t\tif rel.Relation == "owner" {\n\t\t\tnewRelationships = append(newRelationships, rel)\n\t\t}\n\t}\n\tfor _, relation := range SkillShareUnderlyingRelations(in.Relation) {\n\t\tnewRelationships = append(newRelationships, AuthzRelationship{\n\t\t\tResource: resource,\n\t\t\tRelation: relation,\n\t\t\tSubject:  subject,\n\t\t})\n\t}\n\tif _, err := uc.authz.WriteRelationships(ctx, newRelationships...); err != nil {\n\t\t// Best-effort compensation restores the previous relation set if role\n\t\t// replacement fails after revoke.\n\t\tif len(existing) > 0 {\n\t\t\t_, _ = uc.authz.WriteRelationships(ctx, existing...)\n\t\t}\n''',
)

replace_once(
    "internal/biz/skill_access_policy_test.go",
    '''func TestNormalizeSkillShareRelation(t *testing.T) {\n\tfor _, relation := range []string{SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer} {''',
    '''func TestCollapseSkillShareRelationships(t *testing.T) {\n\trels := []AuthzRelationship{\n\t\t{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "viewer", Subject: AuthzSubjectRef{Type: "user", ID: "reviewer"}},\n\t\t{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "reviewer", Subject: AuthzSubjectRef{Type: "user", ID: "reviewer"}},\n\t\t{Resource: AuthzObjectRef{Type: "skill", ID: "demo"}, Relation: "editor", Subject: AuthzSubjectRef{Type: "group", ID: "dev", Relation: "member"}},\n\t}\n\tshares := CollapseSkillShareRelationships(rels)\n\tif len(shares) != 2 {\n\t\tt.Fatalf("len(shares) = %d, want 2", len(shares))\n\t}\n\tif shares[0].Relation != SkillShareRelationReviewer {\n\t\tt.Fatalf("reviewer relation collapsed to %q", shares[0].Relation)\n\t}\n}\n\nfunc TestNormalizeSkillShareRelation(t *testing.T) {\n\tfor _, relation := range []string{SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer} {''',
)
replace_once(
    "docs/ai/skill-access-policy.md",
    '''| `reviewer` | Reviewer / Publisher | review and publish |''',
    '''| `reviewer` + `viewer` | Reviewer / Publisher | view, review, and publish |''',
)
replace_once(
    "docs/ai/skill-access-policy.md",
    '''Only `skill.manage` may change visibility or create/delete shares. Editors cannot\nexpand access.''',
    '''Only `skill.manage` may view authorization metadata, change visibility, or\ncreate/delete shares. Editors cannot expand access. Share creation has replacement\nsemantics: a subject has one product role, while Reviewer/Publisher is stored as\n`reviewer + viewer` to match the current IAM-owned Schema.''',
)
