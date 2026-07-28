-- Model management V2: separate model identity, serving endpoint and Agent usage profile.
-- IDs are AISphere-owned UUID strings. provider_model_id is the value sent to the
-- upstream API's `model` field and is intentionally not used as an internal key.

CREATE TABLE IF NOT EXISTS aihub_models_v2 (
    id                   VARCHAR(36)  PRIMARY KEY,
    code                 VARCHAR(128) NOT NULL,
    display_name         VARCHAR(256) NOT NULL,
    description          TEXT         NOT NULL DEFAULT '',
    status               VARCHAR(32)  NOT NULL DEFAULT 'active',
    vendor               VARCHAR(64)  NOT NULL,
    family               VARCHAR(128) NOT NULL DEFAULT '',
    model_type           VARCHAR(64)  NOT NULL DEFAULT 'llm',
    capabilities_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    reasoning_json       JSONB        NOT NULL DEFAULT '{}'::jsonb,
    provider_config_json JSONB        NOT NULL DEFAULT '{}'::jsonb,
    owner_type           VARCHAR(32)  NOT NULL DEFAULT 'user',
    owner_id             VARCHAR(128) NOT NULL,
    owner_name           VARCHAR(256) NOT NULL DEFAULT '',
    org_id               VARCHAR(128) NOT NULL,
    project_id           VARCHAR(128) NOT NULL DEFAULT '',
    created_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at           TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at           TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_models_v2_status CHECK (status IN ('active', 'disabled')),
    UNIQUE(org_id, code)
);

CREATE TABLE IF NOT EXISTS aihub_model_endpoints_v2 (
    id                       VARCHAR(36)  PRIMARY KEY,
    model_id                 VARCHAR(36)  NOT NULL REFERENCES aihub_models_v2(id),
    display_name             VARCHAR(256) NOT NULL,
    description              TEXT         NOT NULL DEFAULT '',
    status                   VARCHAR(32)  NOT NULL DEFAULT 'active',
    adapter                  VARCHAR(64)  NOT NULL DEFAULT 'openai_compatible',
    api_format               VARCHAR(64)  NOT NULL DEFAULT 'chat_completions',
    base_url                 VARCHAR(512) NOT NULL,
    provider_model_id        VARCHAR(256) NOT NULL,
    api_path                 VARCHAR(256) NOT NULL DEFAULT '',
    credential_ref           VARCHAR(256) NOT NULL DEFAULT '',
    limits_json              JSONB        NOT NULL DEFAULT '{}'::jsonb,
    reasoning_mapping_json   JSONB        NOT NULL DEFAULT '{}'::jsonb,
    request_defaults_json    JSONB        NOT NULL DEFAULT '{}'::jsonb,
    provider_config_json     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    health_status            VARCHAR(32)  NOT NULL DEFAULT 'unknown',
    last_checked_at          TIMESTAMPTZ  NULL,
    org_id                   VARCHAR(128) NOT NULL,
    project_id               VARCHAR(128) NOT NULL DEFAULT '',
    created_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at               TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at               TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_model_endpoints_v2_status CHECK (status IN ('active', 'disabled')),
    CONSTRAINT chk_aihub_model_endpoints_v2_health CHECK (health_status IN ('unknown', 'healthy', 'degraded', 'unhealthy'))
);

CREATE TABLE IF NOT EXISTS aihub_model_profiles_v2 (
    id                      VARCHAR(36)  PRIMARY KEY,
    code                    VARCHAR(128) NOT NULL,
    display_name            VARCHAR(256) NOT NULL,
    description             TEXT         NOT NULL DEFAULT '',
    status                  VARCHAR(32)  NOT NULL DEFAULT 'active',
    endpoint_id             VARCHAR(36)  NOT NULL REFERENCES aihub_model_endpoints_v2(id),
    limits_json             JSONB        NOT NULL DEFAULT '{}'::jsonb,
    reasoning_policy_json   JSONB        NOT NULL DEFAULT '{}'::jsonb,
    default_parameters_json JSONB        NOT NULL DEFAULT '{}'::jsonb,
    allowed_tools_json      JSONB        NOT NULL DEFAULT '[]'::jsonb,
    latest_revision         BIGINT       NOT NULL DEFAULT 1,
    owner_type              VARCHAR(32)  NOT NULL DEFAULT 'user',
    owner_id                VARCHAR(128) NOT NULL,
    owner_name              VARCHAR(256) NOT NULL DEFAULT '',
    org_id                  VARCHAR(128) NOT NULL,
    project_id              VARCHAR(128) NOT NULL DEFAULT '',
    created_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    deleted_at              TIMESTAMPTZ  NULL,
    CONSTRAINT chk_aihub_model_profiles_v2_status CHECK (status IN ('active', 'disabled')),
    UNIQUE(org_id, code)
);

CREATE TABLE IF NOT EXISTS aihub_model_profile_revisions_v2 (
    id            BIGSERIAL    PRIMARY KEY,
    profile_id    VARCHAR(36)  NOT NULL REFERENCES aihub_model_profiles_v2(id) ON DELETE CASCADE,
    revision      BIGINT       NOT NULL,
    snapshot_json JSONB        NOT NULL,
    sha256        VARCHAR(64)  NOT NULL,
    author        VARCHAR(128) NOT NULL,
    commit_msg    TEXT         NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    UNIQUE(profile_id, revision)
);

CREATE INDEX IF NOT EXISTS idx_aihub_models_v2_org ON aihub_models_v2(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_endpoints_v2_model ON aihub_model_endpoints_v2(model_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_endpoints_v2_org ON aihub_model_endpoints_v2(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_profiles_v2_endpoint ON aihub_model_profiles_v2(endpoint_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_profiles_v2_org ON aihub_model_profiles_v2(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_model_profile_revisions_v2_profile ON aihub_model_profile_revisions_v2(profile_id, revision DESC);

COMMENT ON COLUMN aihub_model_endpoints_v2.provider_model_id IS
    'The upstream/provider model identifier sent in the request model field; never an AISphere resource ID.';
COMMENT ON COLUMN aihub_models_v2.reasoning_json IS
    'Provider-neutral reasoning capability: supported modes, effort levels, defaults, budget and reasoning-content preservation.';
COMMENT ON COLUMN aihub_model_endpoints_v2.reasoning_mapping_json IS
    'Provider-specific translation from normalized reasoning policy to request fields, e.g. thinking.type, reasoning_effort or chat_template_kwargs.enable_thinking.';
COMMENT ON COLUMN aihub_model_profile_revisions_v2.snapshot_json IS
    'Immutable fully-resolved Runtime snapshot including model, endpoint, normalized reasoning policy and provider request patch.';
