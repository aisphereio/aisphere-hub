package server

import (
	"context"
	"fmt"
	"strings"

	"github.com/aisphereio/kernel/authn"
	"github.com/aisphereio/kernel/errorx"
)

type agentSkillSetRevisionItem struct {
	SkillName   string `gorm:"column:skill_name"`
	Order       int    `gorm:"column:sort_order"`
	Version     string `gorm:"column:version"`
	CommitSHA   string `gorm:"column:commit_sha"`
	TreeSHA     string `gorm:"column:tree_sha"`
	ManifestSHA string `gorm:"column:manifest_sha256"`
}

// loadAgentSkillSetRevision returns an immutable SkillSet member list. Current
// visibility is checked first so historical rows never become an authorization
// bypass after a SkillSet is deleted or hidden from the launcher.
func (h *agentHTTPHandler) loadAgentSkillSetRevision(ctx context.Context, principal authn.Principal, binding agentSkillSetBinding) ([]agentSkillSetRevisionItem, error) {
	name := strings.TrimSpace(binding.Name)
	if !skillSetNameRE.MatchString(name) {
		return nil, errorx.BadRequest("AGENT_SKILLSET_INVALID", "definition.skillsets contains an invalid skillset name")
	}
	if binding.Revision <= 0 {
		return nil, errorx.BadRequest("AGENT_SKILLSET_REVISION_REQUIRED", "definition.skillsets.revision must be a positive exact revision")
	}

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
	if len(items) == 0 {
		return nil, errorx.Conflict("AGENT_SKILLSET_EMPTY", fmt.Sprintf("skillset revision has no members: %s@%d", name, binding.Revision))
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
