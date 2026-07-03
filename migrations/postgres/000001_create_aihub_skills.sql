-- 000001_create_aihub_skills.sql
-- Canonical Skill + SkillVersion + SkillFile tables.
--
-- This migration is the single source of truth for the skill schema in
-- the new hub. It mirrors the legacy backend's migrations 000002-000005
-- but is consolidated into one file for easier review.

-- Schema search path is implicit (public). All tables are prefixed with
-- aihub_ to namespace them from other modules.

CREATE TABLE IF NOT EXISTS aihub_skills (
  id BIGSERIAL PRIMARY KEY,
  name VARCHAR(128) NOT NULL UNIQUE,
  display_name VARCHAR(256) NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  version VARCHAR(64) NOT NULL DEFAULT '',
  status VARCHAR(32) NOT NULL DEFAULT 'active',
  visibility VARCHAR(32) NOT NULL DEFAULT 'private',
  owner_id VARCHAR(128) NOT NULL DEFAULT '',
  org_id VARCHAR(128) NOT NULL DEFAULT '',
  project_id VARCHAR(128) NOT NULL DEFAULT '',
  source_type VARCHAR(32) NOT NULL DEFAULT '',
  source_uri TEXT NOT NULL DEFAULT '',
  manifest_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  tags JSONB NOT NULL DEFAULT '[]'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_aihub_skills_org_id ON aihub_skills(org_id);
CREATE INDEX IF NOT EXISTS idx_aihub_skills_status ON aihub_skills(status);
CREATE INDEX IF NOT EXISTS idx_aihub_skills_visibility ON aihub_skills(visibility);
CREATE INDEX IF NOT EXISTS idx_aihub_skills_owner_id ON aihub_skills(owner_id);
CREATE INDEX IF NOT EXISTS idx_aihub_skills_deleted_at ON aihub_skills(deleted_at);

CREATE TABLE IF NOT EXISTS aihub_skill_versions (
  id BIGSERIAL PRIMARY KEY,
  skill_name VARCHAR(128) NOT NULL,
  version VARCHAR(64) NOT NULL,
  status VARCHAR(32) NOT NULL DEFAULT 'draft',
  author VARCHAR(128) NOT NULL DEFAULT '',
  commit_msg TEXT NOT NULL DEFAULT '',
  publish_pipeline_info TEXT NOT NULL DEFAULT '',
  download_count BIGINT NOT NULL DEFAULT 0,
  md5 VARCHAR(64) NOT NULL DEFAULT '',
  sha256 VARCHAR(128) NOT NULL DEFAULT '',
  revision TEXT NOT NULL DEFAULT '',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  manifest_json JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  CONSTRAINT uq_aihub_skill_versions_name_version UNIQUE (skill_name, version)
);

CREATE INDEX IF NOT EXISTS idx_aihub_skill_versions_skill_name ON aihub_skill_versions(skill_name);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_versions_status ON aihub_skill_versions(status);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_versions_deleted_at ON aihub_skill_versions(deleted_at);

CREATE TABLE IF NOT EXISTS aihub_skill_files (
  id BIGSERIAL PRIMARY KEY,
  skill_name VARCHAR(128) NOT NULL,
  version VARCHAR(64) NOT NULL,
  path VARCHAR(512) NOT NULL,
  name VARCHAR(256) NOT NULL DEFAULT '',
  type VARCHAR(128) NOT NULL DEFAULT '',
  size BIGINT NOT NULL DEFAULT 0,
  "binary" BOOLEAN NOT NULL DEFAULT false,
  content TEXT NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  CONSTRAINT uq_aihub_skill_files_name_version_path UNIQUE (skill_name, version, path)
);

CREATE INDEX IF NOT EXISTS idx_aihub_skill_files_name_version ON aihub_skill_files(skill_name, version);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_files_deleted_at ON aihub_skill_files(deleted_at);

-- updated_at auto-touch trigger function (shared across all aihub_ tables).
-- We define it here so subsequent migrations can reuse it without redefining.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION aihub_set_updated_at() RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

DROP TRIGGER IF EXISTS trg_aihub_skills_updated_at ON aihub_skills;
CREATE TRIGGER trg_aihub_skills_updated_at
BEFORE UPDATE ON aihub_skills
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();

DROP TRIGGER IF EXISTS trg_aihub_skill_versions_updated_at ON aihub_skill_versions;
CREATE TRIGGER trg_aihub_skill_versions_updated_at
BEFORE UPDATE ON aihub_skill_versions
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();

DROP TRIGGER IF EXISTS trg_aihub_skill_files_updated_at ON aihub_skill_files;
CREATE TRIGGER trg_aihub_skill_files_updated_at
BEFORE UPDATE ON aihub_skill_files
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();
