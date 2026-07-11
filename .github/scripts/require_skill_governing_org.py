from pathlib import Path


def replace_once(path: str, old: str, new: str) -> None:
    p = Path(path)
    text = p.read_text(encoding="utf-8")
    count = text.count(old)
    if count != 1:
        raise SystemExit(f"{path}: expected one match, found {count}: {old[:120]!r}")
    p.write_text(text.replace(old, new, 1), encoding="utf-8")


replace_once(
    "internal/biz/skill.go",
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {\n\t\treturn nil, err\n\t}\n\tout, err = uc.repo.UpdateSkillVisibility(ctx, name, visibility)''',
    '''\tif err := uc.requireSkillPermission(ctx, principal, name, "manage"); err != nil {\n\t\treturn nil, err\n\t}\n\tif visibility == SkillVisibilityInternal {\n\t\tcurrent, getErr := uc.repo.GetSkill(ctx, name)\n\t\tif getErr != nil {\n\t\t\treturn nil, getErr\n\t\t}\n\t\tif strings.TrimSpace(current.OrgID) == "" {\n\t\t\treturn nil, errorx.From(ErrSkillInvalidArgument, errorx.WithMessage("internal visibility requires a governing org_id; backfill the Skill owner organization first"))\n\t\t}\n\t}\n\tout, err = uc.repo.UpdateSkillVisibility(ctx, name, visibility)''',
)
replace_once(
    "docs/ai/skill-access-policy.md",
    '''## Consistency and failure behavior\n''',
    '''## Existing data migration\n\nOlder root Skills may have an empty `org_id` because the previous creation helper\ncleared all placement fields. They continue to work as Private/Public resources,\nbut the backend rejects switching them to Internal until an administrator backfills\nthe governing organization from the stable owner identity/IAM directory.\n\n## Consistency and failure behavior\n''',
)
