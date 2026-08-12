package data

import (
	"context"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/dbx"
)

// agentSkillRefsRepo implements biz.AgentReferenceReader against the Hub
// Agent catalog. It finds Agents whose immutable definition binds a given
// skill directly or through a pinned SkillSet, so SkillUsecase.DeleteSkill can
// refuse to delete a skill that is still in use.
type agentSkillRefsRepo struct {
	db dbx.DB
}

// NewAgentSkillRefsRepo builds the repo from shared Resources.
func NewAgentSkillRefsRepo(resources *Resources) biz.AgentReferenceReader {
	return &agentSkillRefsRepo{db: resources.DB}
}

func (r *agentSkillRefsRepo) ListSkillReferences(ctx context.Context, skillName string) ([]biz.AgentSkillReference, error) {
	type row struct {
		AgentID       string `gorm:"column:agent_id"`
		DisplayName   string `gorm:"column:display_name"`
		LatestVersion string `gorm:"column:latest_version"`
	}
	var rows []row
	if err := r.db.GORM(ctx).Raw(`
		SELECT a.agent_id, a.display_name, a.latest_version
		FROM aihub_agents a
		WHERE a.deleted_at IS NULL
		  AND (
			EXISTS (
			SELECT 1 FROM aihub_agent_versions v
			WHERE v.agent_id = a.agent_id
			  AND EXISTS (
				SELECT 1 FROM jsonb_array_elements(v.definition_json -> 'skills') s
				WHERE s ->> 'name' = ?
			  )
			)
			OR EXISTS (
			  SELECT 1 FROM aihub_agent_versions v
			  JOIN LATERAL jsonb_array_elements(COALESCE(v.definition_json -> 'skillsets', v.definition_json -> 'skillSets', '[]'::jsonb)) ss ON TRUE
			  JOIN aihub_skillset_items i ON i.skillset_name = ss ->> 'name'
			  WHERE v.agent_id = a.agent_id AND i.skill_name = ?
			)
		  )
	`, skillName, skillName).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]biz.AgentSkillReference, 0, len(rows))
	for _, rw := range rows {
		out = append(out, biz.AgentSkillReference{
			AgentID:       rw.AgentID,
			DisplayName:   rw.DisplayName,
			LatestVersion: rw.LatestVersion,
		})
	}
	return out, nil
}
