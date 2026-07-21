-- V2 repository-backed Skill model.
-- Soft Serve owns repos and creates it before Hub migrations run.

CREATE TABLE IF NOT EXISTS hub_skill_profiles (
    repository_id      BIGINT PRIMARY KEY REFERENCES repos(id) ON DELETE CASCADE,
    display_name       VARCHAR(256) NOT NULL DEFAULT '',
    org_id             VARCHAR(128) NOT NULL,
    project_id         VARCHAR(128) NOT NULL DEFAULT '',
    created_by_type    VARCHAR(32) NOT NULL DEFAULT 'user',
    created_by_id      VARCHAR(128) NOT NULL,
    visibility         VARCHAR(32) NOT NULL DEFAULT 'private',
    lifecycle_status   VARCHAR(32) NOT NULL DEFAULT 'provisioning',
    default_branch     VARCHAR(128) NOT NULL DEFAULT 'main',
    provision_error    TEXT NOT NULL DEFAULT '',
    created_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_hub_skill_profiles_visibility
        CHECK (visibility IN ('private', 'internal', 'public')),
    CONSTRAINT chk_hub_skill_profiles_status
        CHECK (lifecycle_status IN ('provisioning', 'active', 'failed', 'deleting'))
);

CREATE INDEX IF NOT EXISTS idx_hub_skill_profiles_org
    ON hub_skill_profiles(org_id);
CREATE INDEX IF NOT EXISTS idx_hub_skill_profiles_project
    ON hub_skill_profiles(project_id);
CREATE INDEX IF NOT EXISTS idx_hub_skill_profiles_creator
    ON hub_skill_profiles(created_by_type, created_by_id);
CREATE INDEX IF NOT EXISTS idx_hub_skill_profiles_visibility_status
    ON hub_skill_profiles(visibility, lifecycle_status);

DROP TRIGGER IF EXISTS trg_hub_skill_profiles_updated_at ON hub_skill_profiles;
CREATE TRIGGER trg_hub_skill_profiles_updated_at
BEFORE UPDATE ON hub_skill_profiles
FOR EACH ROW EXECUTE FUNCTION hub_set_updated_at();

-- Compatibility backfill. Rows without a real Soft Serve repository are left
-- in the legacy table and deliberately do not become active V2 Skills.
INSERT INTO hub_skill_profiles (
    repository_id,
    display_name,
    org_id,
    project_id,
    created_by_type,
    created_by_id,
    visibility,
    lifecycle_status,
    default_branch,
    created_at,
    updated_at
)
SELECT
    r.id,
    s.display_name,
    s.org_id,
    s.project_id,
    'user',
    s.owner_id,
    s.visibility,
    s.status,
    s.default_branch,
    s.created_at,
    s.updated_at
FROM skills s
JOIN repos r ON r.name = s.name
ON CONFLICT (repository_id) DO NOTHING;

-- Pull requests and SkillSets now reference the canonical repository name.
ALTER TABLE skill_pull_requests
    DROP CONSTRAINT IF EXISTS skill_pull_requests_skill_name_fkey;
ALTER TABLE skill_pull_requests
    ADD CONSTRAINT fk_skill_pull_requests_repo
    FOREIGN KEY (skill_name) REFERENCES repos(name) ON DELETE CASCADE;

ALTER TABLE aihub_skillset_items
    DROP CONSTRAINT IF EXISTS fk_aihub_skillset_items_skill;
ALTER TABLE aihub_skillset_items
    ADD CONSTRAINT fk_aihub_skillset_items_repo
    FOREIGN KEY (skill_name) REFERENCES repos(name) ON DELETE CASCADE;

-- +goose StatementBegin
COMMENT ON TABLE hub_skill_profiles IS
    'Hub-owned business metadata extending the canonical Soft Serve repos row.';
COMMENT ON COLUMN hub_skill_profiles.repository_id IS
    'Canonical Skill identity; one-to-one with Soft Serve repos.id.';
COMMENT ON COLUMN hub_skill_profiles.lifecycle_status IS
    'Provisioning state used by Saga compensation and Git protocol gating.';
-- +goose StatementEnd
