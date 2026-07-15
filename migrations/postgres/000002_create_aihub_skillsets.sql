-- 000002_create_aihub_skillsets.sql
-- Lightweight SkillSet metadata plus ordered Skill references.
-- Skill releases remain fully independent; no version, label, runtime or
-- required/optional state is stored on membership rows.

CREATE TABLE IF NOT EXISTS aihub_skill_sets (
  id BIGSERIAL PRIMARY KEY,
  name VARCHAR(128) NOT NULL UNIQUE,
  display_name VARCHAR(256) NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  visibility VARCHAR(32) NOT NULL DEFAULT 'private',
  owner_id VARCHAR(128) NOT NULL DEFAULT '',
  org_id VARCHAR(128) NOT NULL DEFAULT '',
  labels JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_aihub_skill_sets_visibility ON aihub_skill_sets(visibility);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_sets_owner_id ON aihub_skill_sets(owner_id);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_sets_org_id ON aihub_skill_sets(org_id);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_sets_deleted_at ON aihub_skill_sets(deleted_at);

CREATE TABLE IF NOT EXISTS aihub_skill_set_items (
  id BIGSERIAL PRIMARY KEY,
  skill_set_id BIGINT NOT NULL REFERENCES aihub_skill_sets(id) ON DELETE CASCADE,
  skill_name VARCHAR(128) NOT NULL REFERENCES aihub_skills(name),
  sort_order INTEGER NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT uq_aihub_skill_set_items_set_skill UNIQUE (skill_set_id, skill_name)
);

CREATE INDEX IF NOT EXISTS idx_aihub_skill_set_items_set_order
  ON aihub_skill_set_items(skill_set_id, sort_order, skill_name);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_set_items_skill_name
  ON aihub_skill_set_items(skill_name);

DROP TRIGGER IF EXISTS trg_aihub_skill_sets_updated_at ON aihub_skill_sets;
CREATE TRIGGER trg_aihub_skill_sets_updated_at
BEFORE UPDATE ON aihub_skill_sets
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();

DROP TRIGGER IF EXISTS trg_aihub_skill_set_items_updated_at ON aihub_skill_set_items;
CREATE TRIGGER trg_aihub_skill_set_items_updated_at
BEFORE UPDATE ON aihub_skill_set_items
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();
