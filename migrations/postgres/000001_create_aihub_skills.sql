-- Destructive Git-native baseline. Existing package/version/draft tables are
-- intentionally not migrated; deploy this schema into a clean Hub database.

CREATE TABLE IF NOT EXISTS skills (
    name            VARCHAR(128) PRIMARY KEY,
    display_name    VARCHAR(256) NOT NULL DEFAULT '',
    description     TEXT NOT NULL DEFAULT '',
    visibility      VARCHAR(32) NOT NULL DEFAULT 'private',
    owner_id        VARCHAR(128) NOT NULL,
    org_id          VARCHAR(128) NOT NULL DEFAULT '',
    project_id      VARCHAR(128) NOT NULL DEFAULT '',
    default_branch  VARCHAR(128) NOT NULL DEFAULT 'main',
    status          VARCHAR(32) NOT NULL DEFAULT 'provisioning',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT chk_skills_visibility CHECK (visibility IN ('private', 'internal', 'public')),
    CONSTRAINT chk_skills_status CHECK (status IN ('provisioning', 'active', 'deleting'))
);

CREATE INDEX IF NOT EXISTS idx_skills_project ON skills(project_id);
CREATE INDEX IF NOT EXISTS idx_skills_owner ON skills(owner_id);
CREATE INDEX IF NOT EXISTS idx_skills_visibility_status ON skills(visibility, status);

CREATE TABLE IF NOT EXISTS skill_pull_requests (
    id              VARCHAR(36) PRIMARY KEY,
    skill_name      VARCHAR(128) NOT NULL REFERENCES skills(name) ON DELETE CASCADE,
    source_ref      VARCHAR(512) NOT NULL,
    target_ref      VARCHAR(512) NOT NULL DEFAULT 'refs/heads/main',
    source_sha      VARCHAR(64) NOT NULL,
    target_sha      VARCHAR(64) NOT NULL,
    title           VARCHAR(512) NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    state           VARCHAR(32) NOT NULL DEFAULT 'open',
    author_id       VARCHAR(128) NOT NULL,
    merged_by       VARCHAR(128) NOT NULL DEFAULT '',
    merged_sha      VARCHAR(64) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    merged_at       TIMESTAMPTZ,
    CONSTRAINT chk_skill_pr_state CHECK (state IN ('open', 'closed', 'merged'))
);

CREATE INDEX IF NOT EXISTS idx_skill_pr_skill_state ON skill_pull_requests(skill_name, state, created_at DESC);

CREATE TABLE IF NOT EXISTS skill_pull_request_reviews (
    id                VARCHAR(36) PRIMARY KEY,
    pull_request_id   VARCHAR(36) NOT NULL REFERENCES skill_pull_requests(id) ON DELETE CASCADE,
    reviewer_id       VARCHAR(128) NOT NULL,
    verdict           VARCHAR(32) NOT NULL,
    comment           TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_skill_pr_review_reviewer UNIQUE (pull_request_id, reviewer_id),
    CONSTRAINT chk_skill_pr_review_verdict CHECK (verdict IN ('approve', 'request_changes'))
);

CREATE OR REPLACE FUNCTION hub_set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_skills_updated_at ON skills;
CREATE TRIGGER trg_skills_updated_at
BEFORE UPDATE ON skills
FOR EACH ROW EXECUTE FUNCTION hub_set_updated_at();

DROP TRIGGER IF EXISTS trg_skill_pull_requests_updated_at ON skill_pull_requests;
CREATE TRIGGER trg_skill_pull_requests_updated_at
BEFORE UPDATE ON skill_pull_requests
FOR EACH ROW EXECUTE FUNCTION hub_set_updated_at();
