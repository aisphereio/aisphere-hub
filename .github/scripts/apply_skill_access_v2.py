from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    file = Path(path)
    text = file.read_text(encoding="utf-8")
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected exactly one match, found {count}: {old[:100]!r}")
    file.write_text(text.replace(old, new, 1), encoding="utf-8")


def replace_all(path: str, old: str, new: str, expected: int | None = None) -> None:
    file = Path(path)
    text = file.read_text(encoding="utf-8")
    count = text.count(old)
    if expected is not None and count != expected:
        raise SystemExit(f"{path}: expected {expected} matches, found {count}: {old[:100]!r}")
    if count == 0:
        raise SystemExit(f"{path}: no matches: {old[:100]!r}")
    file.write_text(text.replace(old, new), encoding="utf-8")


# Root-catalog policy helpers: retain a governance org without making it a
# directory parent; add internal visibility and align grantable roles with the
# IAM-owned SpiceDB skill definition.
replace_once(
    "internal/biz/skill_access_policy.go",
    '''\tSkillShareRelationViewer = "viewer"\n\tSkillShareRelationEditor = "editor"''',
    '''\tSkillShareRelationViewer   = "viewer"\n\tSkillShareRelationEditor   = "editor"\n\tSkillShareRelationReviewer = "reviewer"\n\n\t// SkillVisibilityInternal means every authenticated principal whose\n\t// stable IAM org_id matches Skill.OrgID can discover and read the Skill.\n\t// The Skill still lives in the global root catalog; OrgID is a governance\n\t// and access boundary, not a catalog parent.\n\tSkillVisibilityInternal = "internal"''',
)
replace_once(
    "internal/biz/skill_access_policy.go",
    '''// Hub does not create Skills under a Hub organization/group/project. The\n// authenticated principal becomes the owner and org/project placement is not\n// accepted from client payloads. Keeping this as a small helper makes the\n// product rule reusable by service code, upload/import code, and tests.''',
    '''// Hub does not create Skills under a Hub organization/group/project. The\n// authenticated principal becomes the owner; principal.OrgID is retained only\n// as the governing organization used by internal visibility, IAM directory\n// pickers, audit, and future quota policy. Project placement is never accepted\n// from client payloads. Keeping this as a small helper makes the product rule\n// reusable by service code, upload/import code, and tests.''',
)
replace_once(
    "internal/biz/skill_access_policy.go",
    '''\tout.OwnerID = ""\n\tout.OrgID = ""\n\tout.ProjectID = ""\n\tif principal.IsAuthenticated() {\n\t\tout.OwnerID = principal.SubjectID\n\t}''',
    '''\tout.OwnerID = ""\n\tout.OrgID = ""\n\tout.ProjectID = ""\n\tif principal.IsAuthenticated() {\n\t\tout.OwnerID = principal.SubjectID\n\t\tout.OrgID = principal.OrgID\n\t}''',
)
replace_once(
    "internal/biz/skill_access_policy.go",
    '''// NormalizeSkillShareRelation accepts only the two grantable relations for\n// user-managed Skill sharing. Ownership is intentionally not transferable from\n// the share dialog.\nfunc NormalizeSkillShareRelation(relation string) (string, error) {\n\trel := strings.ToLower(strings.TrimSpace(relation))\n\tif rel == SkillShareRelationViewer || rel == SkillShareRelationEditor {\n\t\treturn rel, nil\n\t}\n\treturn "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer' or 'editor'"))\n}''',
    '''// NormalizeSkillShareRelation accepts the grantable relations defined by\n// the IAM-owned SpiceDB skill model. Ownership is intentionally not transferable\n// from the share dialog. reviewer is the publish/review role; editor cannot\n// change visibility or grant other users access.\nfunc NormalizeSkillShareRelation(relation string) (string, error) {\n\trel := strings.ToLower(strings.TrimSpace(relation))\n\tswitch rel {\n\tcase SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer:\n\t\treturn rel, nil\n\tdefault:\n\t\treturn "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer', 'editor', or 'reviewer'"))\n\t}\n}\n\n// NormalizeSkillVisibility validates the three product visibility states.\nfunc NormalizeSkillVisibility(visibility string) (string, error) {\n\tv := strings.ToLower(strings.TrimSpace(visibility))\n\tswitch v {\n\tcase SkillVisibilityPrivate, SkillVisibilityInternal, SkillVisibilityPublic:\n\t\treturn v, nil\n\tdefault:\n\t\treturn "", errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private', 'internal', or 'public'"))\n\t}\n}\n\n// CanReadSkillByImplicitPolicy evaluates the durable Hub-side fallbacks used\n// when IAM/SpiceDB has no explicit relation or is temporarily unavailable.\n// It never grants write, publish, share, visibility, or delete permissions.\nfunc CanReadSkillByImplicitPolicy(principal authn.Principal, skill *Skill) bool {\n\tif skill == nil || !principal.IsAuthenticated() {\n\t\treturn false\n\t}\n\tif skill.OwnerID != "" && skill.OwnerID == principal.SubjectID {\n\t\treturn true\n\t}\n\tswitch strings.ToLower(strings.TrimSpace(skill.Visibility)) {\n\tcase SkillVisibilityPublic:\n\t\t// Current /v1/skills routes are authenticated, so public means every\n\t\t// platform user. A future anonymous catalog must use separate PUBLIC\n\t\t// routes that expose only online versions.\n\t\treturn true\n\tcase SkillVisibilityInternal:\n\t\treturn skill.OrgID != "" && principal.OrgID != "" && skill.OrgID == principal.OrgID\n\tdefault:\n\t\treturn false\n\t}\n}''',
)

# Keep root catalog semantics explicit in comments.
replace_once(
    "internal/biz/skill_root_catalog.go",
    '''// ignores client supplied owner_id/org_id/project_id and stamps ownership from\n// the authenticated principal. Hub does not support placing a Skill under a Hub\n// org/group/project; IAM/Casdoor groups are authorization subjects only.''',
    '''// ignores client supplied owner_id/org_id/project_id, stamps ownership from\n// the authenticated principal, and records principal.OrgID as a governance and\n// internal-visibility boundary. Hub does not place a Skill under a Hub\n// org/group/project; IAM/Casdoor groups remain authorization subjects only.''',
)

# Align the core usecase with the IAM schema and the three-state visibility
# policy. Exact replacements intentionally fail if the source has drifted.
replace_once(
    "internal/biz/skill.go",
    '''// UpdateSkillVisibility changes a skill between private and public. Requires\n// skill.edit because it changes who can read the resource.''',
    '''// UpdateSkillVisibility changes a skill between private, internal, and public.\n// It requires skill.manage because visibility changes the resource trust boundary.''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tvisibility = normalizeSkillVisibility(visibility)\n\tif visibility != SkillVisibilityPrivate && visibility != SkillVisibilityPublic {\n\t\treturn nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("visibility must be 'private' or 'public'"))\n\t}\n\tif err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {''',
    '''\tvisibility, err = NormalizeSkillVisibility(visibility)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {''',
)
replace_once(
    "internal/biz/skill.go",
    '''// skill.delete permission via authz. After the DB delete succeeds,''',
    '''// skill.manage permission via authz. After the DB delete succeeds,''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "delete"); err != nil {''',
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tRelation        string // "viewer" | "editor" (NOT "owner")''',
    '''\tRelation        string // "viewer" | "editor" | "reviewer" (NOT "owner")''',
)
replace_once(
    "internal/biz/skill.go",
    '''// Returns owner / viewer / editor relationships. The owner relation is''',
    '''// Returns owner / viewer / editor / reviewer relationships. The owner relation is''',
)
replace_once(
    "internal/biz/skill.go",
    '''// CreateSkillShare grants a viewer or editor relation on the named skill\n// to a subject. Requires skill.edit. The relation field accepts only\n// "viewer" or "editor"; passing "owner" returns ErrSkillInvalidArgument''',
    '''// CreateSkillShare grants a viewer, editor, or reviewer relation on the named\n// skill to a subject. Requires skill.manage. Passing "owner" returns\n// ErrSkillInvalidArgument''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tin.Relation = strings.TrimSpace(in.Relation)\n\tif in.Relation != "viewer" && in.Relation != "editor" {\n\t\treturn nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("relation must be 'viewer' or 'editor' (owner is not transferable)"))\n\t}''',
    '''\tin.Relation, err = NormalizeSkillShareRelation(in.Relation)\n\tif err != nil {\n\t\treturn nil, err\n\t}''',
)
replace_once(
    "internal/biz/skill.go",
    '''\tif err := uc.requireSkillPermission(ctx, principal, in.Name, "edit"); err != nil {\n\t\treturn nil, err\n\t}\n\tif uc.authz == nil {''',
    '''\tif err := uc.requireSkillPermission(ctx, principal, in.Name, "manage"); err != nil {\n\t\treturn nil, err\n\t}\n\tif uc.authz == nil {''',
)
replace_once(
    "internal/biz/skill.go",
    '''// named subject (viewer, editor, but NOT owner — owner can only be\n// removed by deleting the skill). Requires skill.edit.''',
    '''// named subject (viewer, editor, reviewer, but NOT owner — owner can only be\n// removed by deleting the skill). Requires skill.manage.''',
)
# This is the second share-management check; the update-visibility edit check was
# already replaced above and normal editing remains skill.edit.
replace_once(
    "internal/biz/skill.go",
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "edit"); err != nil {\n\t\treturn err\n\t}\n\tif uc.authz == nil {\n\t\treturn nil // dev mode: nothing to delete''',
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {\n\t\treturn err\n\t}\n\tif uc.authz == nil {\n\t\treturn nil // dev mode: nothing to delete''',
)
replace_once(
    "internal/biz/skill.go",
    '''// canReadSkill returns true if principal can read the given skill,\n// considering (1) authz allow, (2) ownership fallback, (3) public\n// visibility fallback. Used by GetSkill and requireSkillRead.\nfunc (uc *SkillUsecase) canReadSkill(ctx context.Context, principal authn.Principal, skill *Skill) bool {\n\tif skill == nil {\n\t\treturn false\n\t}\n\t// Public skills are world-readable by any authenticated user.\n\tif skill.Visibility == SkillVisibilityPublic {\n\t\treturn true\n\t}\n\t// Owner can always read their own skill.\n\tif principal.IsAuthenticated() && skill.OwnerID != "" && skill.OwnerID == principal.SubjectID {\n\t\treturn true\n\t}\n\t// Authz check (may be a re-check in the requireSkillRead path, but''',
    '''// canReadSkill returns true if principal can read the given skill,\n// considering (1) durable owner/internal/public fallbacks and (2) explicit IAM\n// relationships evaluated by SpiceDB. Used by GetSkill and requireSkillRead.\nfunc (uc *SkillUsecase) canReadSkill(ctx context.Context, principal authn.Principal, skill *Skill) bool {\n\tif CanReadSkillByImplicitPolicy(principal, skill) {\n\t\treturn true\n\t}\n\tif skill == nil {\n\t\treturn false\n\t}\n\t// Authz check (may be a re-check in the requireSkillRead path, but''',
)
replace_all(
    "internal/biz/skill.go",
    "ownership / public",
    "ownership / internal / public",
)
replace_all(
    "internal/biz/skill.go",
    "ownership/public",
    "ownership/internal/public",
)
replace_once(
    "internal/biz/skill.go",
    '''// explicit grants should still see public skills and their own skills.''',
    '''// explicit grants should still see internal/public skills and their own skills.''',
)

# Proto comments are part of the product contract; field numbers and generated
# interfaces do not change, so code generation is not required for this slice.
replace_once(
    "api/skill/v1/skill.proto",
    '''  string visibility = 2 [(google.api.field_behavior) = REQUIRED]; // "private" | "public"''',
    '''  string visibility = 2 [(google.api.field_behavior) = REQUIRED]; // "private" | "internal" | "public"''',
)
replace_once(
    "api/skill/v1/skill.proto",
    '''  string relation = 3;        // "viewer" | "editor" | "owner"''',
    '''  string relation = 3;        // "viewer" | "editor" | "reviewer" | "owner"''',
)
replace_once(
    "api/skill/v1/skill.proto",
    '''  string relation = 2 [(google.api.field_behavior) = REQUIRED];  // "viewer" | "editor"''',
    '''  string relation = 2 [(google.api.field_behavior) = REQUIRED];  // "viewer" | "editor" | "reviewer"''',
)

Path("internal/biz/skill_access_policy_test.go").write_text(
    '''package biz\n\nimport (\n\t"testing"\n\n\t"github.com/aisphereio/kernel/authn"\n)\n\nfunc TestNormalizeRootSkillCreateKeepsGovernanceOrg(t *testing.T) {\n\tprincipal := authn.Principal{SubjectID: "user-1", SubjectType: "user", OrgID: "aisphere"}\n\tout := NormalizeRootSkillCreate(&Skill{OwnerID: "spoofed", OrgID: "other", ProjectID: "project-1"}, principal)\n\tif out.OwnerID != "user-1" {\n\t\tt.Fatalf("OwnerID = %q, want user-1", out.OwnerID)\n\t}\n\tif out.OrgID != "aisphere" {\n\t\tt.Fatalf("OrgID = %q, want aisphere", out.OrgID)\n\t}\n\tif out.ProjectID != "" {\n\t\tt.Fatalf("ProjectID = %q, want empty root placement", out.ProjectID)\n\t}\n}\n\nfunc TestCanReadSkillByImplicitPolicy(t *testing.T) {\n\towner := authn.Principal{SubjectID: "owner", SubjectType: "user", OrgID: "org-a"}\n\tmember := authn.Principal{SubjectID: "member", SubjectType: "user", OrgID: "org-a"}\n\toutsider := authn.Principal{SubjectID: "outsider", SubjectType: "user", OrgID: "org-b"}\n\n\tcases := []struct {\n\t\tname      string\n\t\tprincipal authn.Principal\n\t\tskill     *Skill\n\t\twant      bool\n\t}{\n\t\t{"owner private", owner, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPrivate}, true},\n\t\t{"member internal", member, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityInternal}, true},\n\t\t{"outsider internal", outsider, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityInternal}, false},\n\t\t{"authenticated public", outsider, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPublic}, true},\n\t\t{"private without grant", member, &Skill{OwnerID: "owner", OrgID: "org-a", Visibility: SkillVisibilityPrivate}, false},\n\t\t{"anonymous public", authn.Anonymous(), &Skill{Visibility: SkillVisibilityPublic}, false},\n\t}\n\n\tfor _, tc := range cases {\n\t\tt.Run(tc.name, func(t *testing.T) {\n\t\t\tif got := CanReadSkillByImplicitPolicy(tc.principal, tc.skill); got != tc.want {\n\t\t\t\tt.Fatalf("CanReadSkillByImplicitPolicy() = %v, want %v", got, tc.want)\n\t\t\t}\n\t\t})\n\t}\n}\n\nfunc TestNormalizeSkillShareRelation(t *testing.T) {\n\tfor _, relation := range []string{SkillShareRelationViewer, SkillShareRelationEditor, SkillShareRelationReviewer} {\n\t\tif got, err := NormalizeSkillShareRelation(relation); err != nil || got != relation {\n\t\t\tt.Fatalf("NormalizeSkillShareRelation(%q) = %q, %v", relation, got, err)\n\t\t}\n\t}\n\tif _, err := NormalizeSkillShareRelation("owner"); err == nil {\n\t\tt.Fatal("owner must not be transferable through sharing")\n\t}\n}\n''',
    encoding="utf-8",
)

Path("docs/ai/skill-access-policy.md").write_text(
    '''# Skill Access Policy\n\nSkill remains a globally named resource in the Hub root catalog. It is not placed\nunder a Hub organization, group, or project. `org_id` is retained as the governing\nIAM/Casdoor organization for access scope, directory selection, audit, and future\nquota policy.\n\n## Visibility\n\n| Value | Discovery and read policy | Authentication |\n| --- | --- | --- |\n| `private` | owner plus explicit IAM/SpiceDB grants | required |\n| `internal` | private policy plus principals whose stable `org_id` equals the Skill governing org | required |\n| `public` | every authenticated platform principal | required on current `/v1/skills/*` routes |\n\n`public` is intentionally platform-public in this slice because all current Skill\nroutes are `AUTHENTICATED`. True anonymous distribution must use separate `PUBLIC`\nroutes and expose only online versions, never drafts, shares, or owner metadata.\n\n## IAM roles\n\nHub uses the IAM-owned SpiceDB `skill` definition without maintaining a second\nSchema:\n\n| Relation | Product role | Effective capabilities |\n| --- | --- | --- |\n| `owner` | Owner | manage, edit, review, publish, view |\n| `editor` | Editor | edit and view |\n| `reviewer` | Reviewer / Publisher | review and publish |\n| `viewer` | Viewer / Consumer | view and download |\n\nOnly `skill.manage` may change visibility or create/delete shares. Editors cannot\nexpand access. Group grants target `group:{id}#member`; membership remains owned by\nCasdoor and projected by IAM.\n\n## Consistency and failure behavior\n\n- Durable `owner_id`, `org_id`, and visibility are read-only fallbacks.\n- Explicit user/group grants are evaluated through IAM-managed SpiceDB data.\n- IAM/Authz outages may fall back only to owner/internal/public reads.\n- Write, publish, share, visibility, and delete operations remain fail-closed.\n- Hub never publishes or overwrites the IAM-owned shared Schema.\n''',
    encoding="utf-8",
)
