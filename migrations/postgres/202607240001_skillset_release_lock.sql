-- SkillSet members pin immutable Skill releases so Runtime can reproduce an
-- exact execution environment. Existing rows remain unresolved until an owner
-- explicitly selects a release in the Hub UI.

ALTER TABLE aihub_skillsets
    ADD COLUMN IF NOT EXISTS revision BIGINT NOT NULL DEFAULT 1;

ALTER TABLE aihub_skillset_items
    ADD COLUMN IF NOT EXISTS version VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS commit_sha VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS tree_sha VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS manifest_sha256 VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ NULL;

CREATE INDEX IF NOT EXISTS idx_aihub_skillset_items_version
    ON aihub_skillset_items(skill_name, version);

COMMENT ON COLUMN aihub_skillsets.revision IS
    'Monotonic SkillSet snapshot revision, incremented whenever metadata or members change.';
COMMENT ON COLUMN aihub_skillset_items.version IS
    'Canonical immutable SemVer tag, for example v1.4.0, empty only for legacy unresolved rows.';
COMMENT ON COLUMN aihub_skillset_items.commit_sha IS
    'Exact Git commit resolved from version when the member was written.';
COMMENT ON COLUMN aihub_skillset_items.tree_sha IS
    'Exact Git tree resolved from version when the member was written.';
COMMENT ON COLUMN aihub_skillset_items.manifest_sha256 IS
    'SHA-256 of SKILL.md at the resolved release commit.';
