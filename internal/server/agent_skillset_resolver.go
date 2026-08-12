package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

// agentSkillSetBinding is always exact. Agent revisions must never point at the
// mutable "latest" SkillSet projection because that would make an old Agent
// revision drift when somebody edits the set later.
type agentSkillSetBinding struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Required bool   `json:"required,omitempty"`
}

type agentSkillSetRevisionItem struct {
	SkillName   string `gorm:"column:skill_name"`
	Order       int    `gorm:"column:sort_order"`
	Version     string `gorm:"column:version"`
	CommitSHA   string `gorm:"column:commit_sha"`
	TreeSHA     string `gorm:"column:tree_sha"`
	ManifestSHA string `gorm:"column:manifest_sha256"`
}

func (h *agentHTTPHandler) loadAgentSkillSetRevision(ctx context.Context, principal authn.Principal, binding agentSkillSetBinding) ([]agentSkillSetRevisionItem, error) {
	name := strings.TrimSpace(binding.Name)
	if !skillSetNameRE.MatchString(name) {
		return nil, errorx.BadRequest("AGENT_SKILLSET_INVALID", "definition.skillSets contains an invalid skillset name")
	}
	if binding.Revision <= 0 {
		return nil, errorx.BadRequest("AGENT_SKILLSET_REVISION_REQUIRED", "definition.skillSets.revision must be a positive exact revision")
	}

	// Visibility is evaluated against the current SkillSet projection. A deleted
	// or no-longer-visible SkillSet cannot be newly bound/resolved merely because
	// an old immutable snapshot still exists.
	var visible int64
	if err := h.db(ctx).Table("aihub_skillsets").
		Where("name = ? AND deleted_at IS NULL", name).
		Where("visibility = 'public' OR owner_id = ? OR (visibility = 'internal' AND org_id <> '' AND org_id = ?)", principal.SubjectID, principal.OrgID).
		Count(&visible).Error; err != nil {
		return nil, agentDBErr(err)
	}
	if visible == 0 {
		return nil, errorx.NotFound("AGENT_SKILLSET_NOT_FOUND", "bound skillset not found or not visible")
	}

	var revisionCount int64
	if err := h.db(ctx).Table("aihub_skillset_revisions").
		Where("skillset_name = ? AND revision = ?", name, binding.Revision).
		Count(&revisionCount).Error; err != nil {
		return nil, agentDBErr(err)
	}
	if revisionCount == 0 {
		return nil, errorx.NotFound("AGENT_SKILLSET_REVISION_NOT_FOUND", fmt.Sprintf("skillset revision not found: %s@%d", name, binding.Revision))
	}

	var items []agentSkillSetRevisionItem
	if err := h.db(ctx).Table("aihub_skillset_revision_items").
		Select("skill_name, sort_order, version, commit_sha, tree_sha, manifest_sha256").
		Where("skillset_name = ? AND revision = ?", name, binding.Revision).
		Order("sort_order ASC, skill_name ASC").
		Find(&items).Error; err != nil {
		return nil, agentDBErr(err)
	}
	for _, item := range items {
		if strings.TrimSpace(item.SkillName) == "" || strings.TrimSpace(item.Version) == "" ||
			strings.TrimSpace(item.CommitSHA) == "" || strings.TrimSpace(item.TreeSHA) == "" ||
			strings.TrimSpace(item.ManifestSHA) == "" {
			return nil, errorx.Conflict(
				"AGENT_SKILLSET_REVISION_INCOMPLETE",
				fmt.Sprintf("skillset revision contains an incompletely pinned historical member: %s@%d/%s", name, binding.Revision, item.SkillName),
			)
		}
	}
	return items, nil
}

func skillSnapshotString(snapshot map[string]any, key string) string {
	if snapshot == nil {
		return ""
	}
	value, ok := snapshot[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func appendSkillProvenance(snapshot map[string]any, provenance map[string]any) {
	if snapshot == nil {
		return
	}
	values, _ := snapshot["provenance"].([]any)
	values = append(values, provenance)
	snapshot["provenance"] = values
}

// mergeAgentSkillSnapshot deduplicates the same exact Skill coming from direct
// binding and/or one or more SkillSets. Different immutable versions of the
// same Skill are rejected instead of relying on ordering to silently choose one.
func mergeAgentSkillSnapshot(out *[]map[string]any, index map[string]int, snapshot map[string]any, provenance map[string]any) error {
	name := skillSnapshotString(snapshot, "name")
	if previousIndex, ok := index[name]; ok {
		previous := (*out)[previousIndex]
		if skillSnapshotString(previous, "version") != skillSnapshotString(snapshot, "version") ||
			skillSnapshotString(previous, "revision") != skillSnapshotString(snapshot, "revision") ||
			skillSnapshotString(previous, "source") != skillSnapshotString(snapshot, "source") {
			return errorx.Conflict(
				"AGENT_SKILL_VERSION_CONFLICT",
				fmt.Sprintf("skill %s resolves to multiple immutable versions (%s/%s vs %s/%s)",
					name,
					skillSnapshotString(previous, "version"), skillSnapshotString(previous, "revision"),
					skillSnapshotString(snapshot, "version"), skillSnapshotString(snapshot, "revision")),
			)
		}
		appendSkillProvenance(previous, provenance)
		return nil
	}
	appendSkillProvenance(snapshot, provenance)
	index[name] = len(*out)
	*out = append(*out, snapshot)
	return nil
}

// resolveAgentSkills expands direct Skill + SkillSet bindings into one exact,
// deduplicated runtime list. Every SkillSet member is re-authorized through the
// same catalog path as a direct binding, so a historical snapshot never becomes
// an authorization bypass.
func (h *agentHTTPHandler) resolveAgentSkills(
	ctx context.Context,
	principal authn.Principal,
	direct []agentSkillBinding,
	sets []agentSkillSetBinding,
	runtimeID string,
) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(direct))
	index := make(map[string]int, len(direct))

	directSnapshots, err := h.resolveAgentSkillSnapshots(ctx, principal, direct, runtimeID)
	if err != nil {
		return nil, err
	}
	for _, snapshot := range directSnapshots {
		if err := mergeAgentSkillSnapshot(&out, index, snapshot, map[string]any{"type": "direct"}); err != nil {
			return nil, err
		}
	}

	seenSets := make(map[string]struct{}, len(sets))
	for _, set := range sets {
		set.Name = strings.TrimSpace(set.Name)
		setKey := fmt.Sprintf("%s@%d", set.Name, set.Revision)
		if _, duplicate := seenSets[setKey]; duplicate {
			return nil, errorx.BadRequest("AGENT_SKILLSET_DUPLICATE", "definition.skillSets contains duplicate "+setKey)
		}
		seenSets[setKey] = struct{}{}

		members, err := h.loadAgentSkillSetRevision(ctx, principal, set)
		if err != nil {
			return nil, err
		}
		for _, member := range members {
			snapshots, err := h.resolveAgentSkillSnapshots(ctx, principal, []agentSkillBinding{{
				Name: member.SkillName, Version: member.Version, Source: "catalog",
			}}, runtimeID)
			if err != nil {
				return nil, err
			}
			if len(snapshots) != 1 {
				return nil, errorx.Internal("AGENT_SKILLSET_RESOLVE_FAILED", "skillset member did not resolve to exactly one skill")
			}
			snapshot := snapshots[0]
			if skillSnapshotString(snapshot, "revision") != member.CommitSHA ||
				skillSnapshotString(snapshot, "treeSha") != member.TreeSHA ||
				skillSnapshotString(snapshot, "manifestSha256") != member.ManifestSHA {
				return nil, errorx.Conflict(
					"AGENT_SKILLSET_REVISION_STALE",
					fmt.Sprintf("immutable skillset member changed unexpectedly: %s@%s", member.SkillName, member.Version),
				)
			}
			if err := mergeAgentSkillSnapshot(&out, index, snapshot, map[string]any{
				"type": "skillset", "name": set.Name, "revision": set.Revision,
			}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
