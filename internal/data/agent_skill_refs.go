package data

import (
	"context"

	"github.com/aisphereio/aisphere-hub/internal/biz"
	"github.com/aisphereio/kernel/dbx"
)

// agentSkillRefsRepo implements biz.AgentReferenceReader against the Hub
// Agent catalog. It finds Agents whose immutable definition binds a given
// skill (definition_json.skills[].name), so SkillUsecase.DeleteSkill can
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
		  AND EXISTS (
			SELECT 1 FROM jsonb_array_elements(a.definition_json -> 'skills') s
			WHERE s ->> 'name' = ?
		  )
	`, skillName).Scan(&rows).Error; err != nil {
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