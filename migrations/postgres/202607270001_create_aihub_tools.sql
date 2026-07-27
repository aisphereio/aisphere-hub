-- Tool catalog: the source of truth for agent-invocable capabilities.
--
-- A Tool is a versioned capability definition (ToolDefinition) that the
-- AISphere Runtime fetches to register MCP tools dynamically. Tool content is
-- immutable per version (aihub_tool_versions); aihub_tools carries the
-- mutable metadata + latest_version pointer.
--
-- Authz uses the SpiceDB `tool` definition (owner/editor/executor/viewer;
-- manage/edit/execute/view; parent tool_space). The object_ref column holds
-- the "tool:{tool_id}" SpiceDB object ref so relationships can be revoked on
-- delete without reconstructing it. Builtin tools (status='builtin',
-- scope='system') are seeded by the Hub process at startup and have no owner
-- SpiceDB relationships.

CREATE TABLE IF NOT EXISTS aihub_tools (
    id             BIGSERIAL    PRIMARY KEY,
    tool_id        VARCHAR(128) NOT NULL UNIQUE,
    display_name   VARCHAR(256) NOT NULL DEFAULT '',
    description    TEXT         NOT NULL DEFAULT '',
    status         VARCHAR(32)  NOT NULL DEFAULT 'active',
    scope          VARCHAR(32)  NOT NULL DEFAULT 'project',
    owner_type     VARCHAR(32)  NOT NULL DEFAULT 'user',
    owner_id       VARCHAR(128) NOT NULL DEFAULT '',
    owner_name     VARCHAR(256) NOT NULL DEFAULT '',
    org_id         VARCHAR(128) NOT NULL DEFAULT '',
    project_id     VARCHAR(128) NOT NULL DEFAULT '',
    latest_version VARCHAR(64)  NOT NULL DEFAULT '',
    labels_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    object_ref     VARCHAR(256) NOT NULL DEFAULT '',
    created_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at     TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_tools_status
        CHECK (status IN ('active', 'disabled', 'builtin')),
    CONSTRAINT chk_aihub_tools_scope
        CHECK (scope IN ('system', 'project', 'private'))
);

CREATE INDEX IF NOT EXISTS idx_aihub_tools_owner
    ON aihub_tools(owner_type, owner_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_tools_org
    ON aihub_tools(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_tools_status_scope
    ON aihub_tools(status, scope) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS aihub_tool_versions (
    id              BIGSERIAL    PRIMARY KEY,
    tool_id         VARCHAR(128) NOT NULL,
    version         VARCHAR(64)  NOT NULL,
    revision        VARCHAR(128) NOT NULL DEFAULT '',
    sha256          VARCHAR(64)  NOT NULL DEFAULT '',
    author          VARCHAR(128) NOT NULL DEFAULT '',
    commit_msg      TEXT         NOT NULL DEFAULT '',
    definition_json JSONB        NOT NULL,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(tool_id, version),
    CONSTRAINT fk_aihub_tool_versions_tool
        FOREIGN KEY (tool_id) REFERENCES aihub_tools(tool_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_aihub_tool_versions_tool
    ON aihub_tool_versions(tool_id, created_at DESC);

-- +goose StatementBegin
COMMENT ON TABLE aihub_tools IS
    'Tool catalog records; each is a versioned agent capability. Builtin tools (status=builtin) are seeded by the Hub and have no SpiceDB owner relationships.';
COMMENT ON COLUMN aihub_tools.object_ref IS
    'SpiceDB object ref "tool:{tool_id}"; used to revoke relationships on delete.';
COMMENT ON COLUMN aihub_tools.labels_json IS
    'Free-form labels (map[string]string); marshaled to JSONB.';
COMMENT ON TABLE aihub_tool_versions IS
    'Immutable per-version ToolDefinition snapshots; definition_json holds the full runtime+execution+schema spec.';
-- +goose StatementEnd
