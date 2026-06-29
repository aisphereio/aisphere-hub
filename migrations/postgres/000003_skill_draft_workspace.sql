-- 000003_skill_draft_workspace.sql
-- Persistent online-editor workspace for Skill drafts.
--
-- Design:
--   * S3 stores file bodies by content hash.
--   * PG stores path/tree metadata, object_key, sha256, author, timestamps.
--   * draft rows are mutable while the corresponding skill version status is
--     draft; commit turns the workspace into an immutable package snapshot.

CREATE TABLE IF NOT EXISTS aihub_skill_draft_files (
  id BIGSERIAL PRIMARY KEY,
  skill_name VARCHAR(128) NOT NULL,
  version VARCHAR(64) NOT NULL,
  path VARCHAR(1024) NOT NULL,
  name VARCHAR(256) NOT NULL DEFAULT '',
  kind VARCHAR(32) NOT NULL DEFAULT 'file',
  content_type VARCHAR(128) NOT NULL DEFAULT '',
  size_bytes BIGINT NOT NULL DEFAULT 0,
  "binary" BOOLEAN NOT NULL DEFAULT false,
  sha256 VARCHAR(128) NOT NULL DEFAULT '',
  object_key TEXT NOT NULL DEFAULT '',
  created_by VARCHAR(128) NOT NULL DEFAULT '',
  updated_by VARCHAR(128) NOT NULL DEFAULT '',
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ,
  CONSTRAINT uq_aihub_skill_draft_files_name_version_path UNIQUE (skill_name, version, path),
  CONSTRAINT ck_aihub_skill_draft_files_kind CHECK (kind IN ('file', 'directory'))
);

CREATE INDEX IF NOT EXISTS idx_aihub_skill_draft_files_name_version ON aihub_skill_draft_files(skill_name, version);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_draft_files_object_key ON aihub_skill_draft_files(object_key) WHERE object_key <> '';
CREATE INDEX IF NOT EXISTS idx_aihub_skill_draft_files_deleted_at ON aihub_skill_draft_files(deleted_at);
CREATE INDEX IF NOT EXISTS idx_aihub_skill_draft_files_path_prefix ON aihub_skill_draft_files(skill_name, version, path text_pattern_ops);

DROP TRIGGER IF EXISTS trg_aihub_skill_draft_files_updated_at ON aihub_skill_draft_files;
CREATE TRIGGER trg_aihub_skill_draft_files_updated_at
BEFORE UPDATE ON aihub_skill_draft_files
FOR EACH ROW EXECUTE FUNCTION aihub_set_updated_at();
