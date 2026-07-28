-- Agent control plane: versioned Agent definitions plus human Tool-consent policy.
--
-- Authorization is deliberately not stored as an IAM grant here. Agent Tool
-- bindings only declare which capabilities the model may request and whether a
-- human approved them permanently (always), per run (per_run), or disabled.
-- Runtime receives the authenticated Principal through the trusted internal
-- context; the target resource service calls IAM for the authoritative decision.

CREATE TABLE IF NOT EXISTS aihub_agents (
    id             BIGSERIAL    PRIMARY KEY,
    agent_id       VARCHAR(128) NOT NULL UNIQUE,
    display_name   VARCHAR(256) NOT NULL DEFAULT '',
    description    TEXT         NOT NULL DEFAULT '',
    status         VARCHAR(32)  NOT NULL DEFAULT 'active',
    scope          VARCHAR(32)  NOT NULL DEFAULT 'project',
    owner_type     VARCHAR(32)  NOT NULL DEFAULT 'user',
    owner_id       VARCHAR(128) NOT NULL DEFAULT '',
    owner_name     VARCHAR(256) NOT NULL DEFAULT '',
    org_id         VARCHAR(128) NOT NULL DEFAULT '',
    project_id     VARCHAR(256) NOT NULL DEFAULT '',
    latest_version VARCHAR(64)  NOT NULL DEFAULT '',
    labels_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    object_ref     VARCHAR(256) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_agents_status CHECK (status IN ('active', 'disabled')),
    CONSTRAINT chk_aihub_agents_scope CHECK (scope IN ('system', 'project', 'private'))
);

CREATE INDEX IF NOT EXISTS idx_aihub_agents_owner
    ON aihub_agents(owner_type, owner_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_agents_org
    ON aihub_agents(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_agents_status_scope
    ON aihub_agents(status, scope) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS aihub_agent_versions (
    id              BIGSERIAL    PRIMARY KEY,
    agent_id        VARCHAR(128) NOT NULL,
    version         VARCHAR(64)  NOT NULL,
    revision        VARCHAR(128) NOT NULL DEFAULT '',
    sha256          VARCHAR(64)  NOT NULL DEFAULT '',
    author          VARCHAR(128) NOT NULL DEFAULT '',
    commit_msg      TEXT         NOT NULL DEFAULT '',
    definition_json JSONB        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(agent_id, version),
    CONSTRAINT fk_aihub_agent_versions_agent
        FOREIGN KEY (agent_id) REFERENCES aihub_agents(agent_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_aihub_agent_versions_agent
    ON aihub_agent_versions(agent_id, created_at DESC);

-- +goose StatementBegin
COMMENT ON TABLE aihub_agents IS
    'Versioned Hub-managed Agent catalog. Tool approval modes are consent metadata, never IAM grants.';
COMMENT ON TABLE aihub_agent_versions IS
    'Immutable Agent definition snapshots. Runtime snapshots contain only approved Tool bindings.';
-- +goose StatementEnd
