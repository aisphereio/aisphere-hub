#!/usr/bin/env python3
from pathlib import Path

path = Path("api/skill/v1/skill.proto")
content = path.read_text(encoding="utf-8")


def replace_once(old: str, new: str) -> None:
    global content
    if old not in content:
        raise RuntimeError(f"expected Skill policy text not found: {old!r}")
    content = content.replace(old, new, 1)


# Root Skill creation/import is open to authenticated platform users. The biz
# layer stamps ownership and enforces edit permission when an upload targets an
# existing Skill.
replace_once(
'''    option (aisphere.access.v1.policy) = {
      exposure: AUTHORIZED
      authz: { action: "create" resource: "aihub:skill:*" audience: "aihub-service" mode: CHECK_ONLY }
      audit: { enabled: true event: "aihub.skill.create" risk: "medium" }
    };
''',
'''    option (aisphere.access.v1.policy) = {
      exposure: AUTHENTICATED
      audit: { enabled: true event: "aihub.skill.create" risk: "medium" }
    };
''')
replace_once(
'''    option (aisphere.access.v1.policy) = {
      exposure: AUTHORIZED
      authz: { action: "upload" resource: "aihub:skill:*" audience: "aihub-service" mode: CHECK_ONLY }
      audit: { enabled: true event: "aihub.skill.upload" risk: "medium" }
    };
''',
'''    option (aisphere.access.v1.policy) = {
      exposure: AUTHENTICATED
      audit: { enabled: true event: "aihub.skill.upload" risk: "medium" }
    };
''')

# Every generated authorization rule must target the IAM-owned `skill` schema.
# Version and draft actions authorize against their parent Skill.
replacements = {
    'authz: { action: "update" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "visibility:update" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "manage" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "delete" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "manage" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "submit" resource: "aihub:skill:{name}:version:{version}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "publish" resource: "aihub:skill:{name}:version:{version}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "publish" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "online" resource: "aihub:skill:{name}:version:{version}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "publish" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "offline" resource: "aihub:skill:{name}:version:{version}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "publish" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "draft:file:write" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "draft:dir:write" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "draft:path:delete" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "draft:path:move" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "draft:commit" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "edit" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "share:list" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "manage" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "share:create" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "manage" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
    'authz: { action: "share:delete" resource: "aihub:skill:{name}" audience: "aihub-service" mode: CHECK_ONLY }':
        'authz: { action: "manage" resource: "skill:{name}" audience: "iam-service" mode: CHECK_ONLY }',
}

for old, new in replacements.items():
    replace_once(old, new)

content = content.replace(
    'skill.edit on the named skill (only the owner / editor can share),\n  // and they only manage viewer / editor relations (NOT owner — owner',
    'skill.manage on the named skill (only the owner can manage shares),\n  // and they manage viewer / editor / reviewer relations (NOT owner — owner',
)
content = content.replace('Requires skill.read (with ownership + public fallback).', 'Requires skill.manage.')
content = content.replace('Requires skill.edit. The relation field accepts', 'Requires skill.manage. The relation field accepts')
content = content.replace('// "viewer" or "editor"; passing "owner" returns INVALID_ARGUMENT.', '// "viewer", "editor", or "reviewer"; passing "owner" returns INVALID_ARGUMENT.')
content = content.replace('the named subject. Requires skill.edit.', 'the named subject. Requires skill.manage.')

path.write_text(content, encoding="utf-8")
print("Skill proto authorization policies aligned with IAM schema")
