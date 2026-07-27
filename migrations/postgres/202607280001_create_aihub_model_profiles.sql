-- Model profile catalog: enterprise-controlled LLM model configurations.
--
-- Hub is the source of truth for model endpoints, protocol, capabilities,
-- credential refs and limits. The AISphere Runtime resolves an immutable
-- ModelProfileRevision to build an ADK model.LLM via the aisphere:// factory.
-- Plain-text credentials never enter Hub; only a credential_ref is stored.
--
-- The profile row carries mutable metadata + latest_revision pointer; the
-- revision row is an immutable snapshot (sha256 of the definition). This
-- mirrors the Skill Release / Tool Version pattern.
--
-- Authz uses the SpiceDB `model_profile` definition (owner/editor/viewer/
-- executor; manage/edit/view/execute; parent project). object_ref holds
-- "model_profile:{profile_id}" so relationships can be revoked on delete.

CREATE TABLE IF NOT EXISTS aihub_model_profiles (
    id              BIGSERIAL    PRIMARY KEY,
    profile_id      VARCHAR(128) NOT NULL UNIQUE,
    display_name    VARCHAR(256) NOT NULL DEFAULT '',
    description     TEXT         NOT NULL DEFAULT '',
    status          VARCHAR(32)  NOT NULL DEFAULT 'active',
    provider        VARCHAR(64)  NOT NULL DEFAULT '',
    api_format      VARCHAR(64)  NOT NULL DEFAULT 'openai_responses',
    endpoint        VARCHAR(256) NOT NULL DEFAULT '',
    model_name      VARCHAR(128) NOT NULL DEFAULT '',
    upstream_model  VARCHAR(128) NOT NULL DEFAULT '',
    upstream_path   VARCHAR(256) NOT NULL DEFAULT '',
    secret_ref      VARCHAR(256) NOT NULL DEFAULT '',
    latest_revision VARCHAR(64)  NOT NULL DEFAULT '',
    owner_type      VARCHAR(32)  NOT NULL DEFAULT 'user',
    owner_id        VARCHAR(128) NOT NULL DEFAULT '',
    owner_name      VARCHAR(256) NOT NULL DEFAULT '',
    org_id          VARCHAR(128) NOT NULL DEFAULT '',
    project_id      VARCHAR(128) NOT NULL DEFAULT '',
    labels_json     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    object_ref      VARCHAR(256) NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at      TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_model_profiles_status
        CHECK (status IN ('active', 'disabled'))
);

CREATE INDEX IF NOT EXISTS idx_aihub_model_profiles_owner
    ON aihub_model_profiles(owner_type, owner_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_profiles_org
    ON aihub_model_profiles(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_profiles_status
    ON aihub_model_profiles(status) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS aihub_model_profile_revisions (
    id                       BIGSERIAL    PRIMARY KEY,
    profile_id              VARCHAR(128) NOT NULL,
    revision                VARCHAR(64)  NOT NULL,
    provider                VARCHAR(64)  NOT NULL,
    api_format              VARCHAR(64)  NOT NULL,
    endpoint                VARCHAR(256) NOT NULL,
    model_name              VARCHAR(128) NOT NULL,
    upstream_model          VARCHAR(128) NOT NULL,
    upstream_path           VARCHAR(256) NOT NULL,
    secret_ref              VARCHAR(256) NOT NULL,
    allowed_tools_json      JSONB        NOT NULL DEFAULT '[]'::jsonb,
    limits_json             JSONB        NOT NULL DEFAULT '{}'::jsonb,
    reasoning_json          JSONB        NOT NULL DEFAULT '{}'::jsonb,
    default_parameters_json JSONB        NOT NULL DEFAULT '{}'::jsonb,
    metadata_json           JSONB        NOT NULL DEFAULT '{}'::jsonb,
    sha256                  VARCHAR(64)  NOT NULL DEFAULT '',
    author                  VARCHAR(128) NOT NULL DEFAULT '',
    commit_msg              TEXT         NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(profile_id, revision),
    CONSTRAINT fk_aihub_model_profile_revisions_profile
        FOREIGN KEY (profile_id) REFERENCES aihub_model_profiles(profile_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_aihub_model_profile_revisions_profile
    ON aihub_model_profile_revisions(profile_id, created_at DESC);

-- +goose StatementBegin
COMMENT ON TABLE aihub_model_profiles IS
    'Enterprise model profile catalog; each is a versioned LLM configuration. Hub owns endpoint/protocol/capability/credential_ref; Runtime resolves immutable revisions.';
COMMENT ON COLUMN aihub_model_profiles.object_ref IS
    'SpiceDB object ref "model_profile:{profile_id}"; used to revoke relationships on delete.';
COMMENT ON COLUMN aihub_model_profiles.secret_ref IS
    'Credential reference (e.g. secret://model/openai-prod, env://VAR). Never a plain-text key.';
COMMENT ON COLUMN aihub_model_profiles.labels_json IS
    'Free-form labels (map[string]string); marshaled to JSONB.';
COMMENT ON TABLE aihub_model_profile_revisions IS
    'Immutable per-revision ModelProfile snapshots; sha256 is the content hash of the revision definition.';
-- +goose StatementEnd
