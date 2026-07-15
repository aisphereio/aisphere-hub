# Lightweight SkillSet

`SkillSet` is a lightweight Hub resource that groups canonical Skills.

## Boundary

A SkillSet stores only:

- name, display name, description and visibility;
- owner and organization metadata;
- ordered references to canonical `Skill.name` values.

A SkillSet does **not**:

- pin or publish Skill versions;
- copy Skill packages;
- own runtime configuration;
- change the visibility or permissions of a referenced Skill;
- contain another SkillSet.

Each Skill keeps its own repository, versions, release lifecycle, authorization and runtime. API responses may expose the Skill's current version for display, but that value is dynamically joined from `aihub_skills` and is never persisted in `aihub_skillset_items`.

## HTTP API

| Method | Path | Description |
|---|---|---|
| GET | `/v1/skillsets` | List visible sets |
| POST | `/v1/skillsets` | Create a set |
| GET | `/v1/skillsets/{name}` | Get set and ordered members |
| PUT | `/v1/skillsets/{name}` | Update metadata or replace members |
| DELETE | `/v1/skillsets/{name}` | Soft-delete a set |
| POST | `/v1/skillsets/{name}/members` | Add or update one Skill reference |
| PUT | `/v1/skillsets/{name}/members/{skill}` | Update member order |
| DELETE | `/v1/skillsets/{name}/members/{skill}` | Remove a Skill reference |
| GET | `/v1/skills/{skill}/skillsets` | Reverse lookup visible sets |

Example:

```json
{
  "name": "office",
  "displayName": "办公工具",
  "description": "PPT、Excel、Word、PDF 等办公类 Skill",
  "members": [
    { "skillName": "ppt", "order": 0 },
    { "skillName": "excel", "order": 1 },
    { "skillName": "word", "order": 2 },
    { "skillName": "pdf", "order": 3 }
  ]
}
```
