-- Lightweight SkillSet: metadata + ordered references to independently versioned Skills.
-- A SkillSet never pins, copies, publishes, or executes a Skill version.

CREATE TABLE IF NOT EXISTS aihub_skillsets (
    id           BIGSERIAL PRIMARY KEY,
    name         VARCHAR(128) NOT NULL UNIQUE,
    display_name VARCHAR(256) NOT NULL DEFAULT '',
    description  TEXT NOT NULL DEFAULT '',
    visibility   VARCHAR(32) NOT NULL DEFAULT 'private',
    owner_id     VARCHAR(128) NOT NULL DEFAULT '',
    org_id       VARCHAR(128) NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at   TIMESTAMPTZ NULL,
    CONSTRAINT chk_aihub_skillsets_visibility
        CHECK (visibility IN ('private', 'internal', 'public'))
);

CREATE INDEX IF NOT EXISTS idx_aihub_skillsets_owner
    ON aihub_skillsets(owner_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_skillsets_org
    ON aihub_skillsets(org_id) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_aihub_skillsets_visibility
    ON aihub_skillsets(visibility) WHERE deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS aihub_skillset_items (
    skillset_name VARCHAR(128) NOT NULL,
    skill_name    VARCHAR(128) NOT NULL,
    sort_order    INTEGER NOT NULL DEFAULT 0,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (skillset_name, skill_name),
    CONSTRAINT fk_aihub_skillset_items_set
        FOREIGN KEY (skillset_name) REFERENCES aihub_skillsets(name) ON DELETE CASCADE,
    CONSTRAINT fk_aihub_skillset_items_skill
        FOREIGN KEY (skill_name) REFERENCES skills(name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_aihub_skillset_items_skill
    ON aihub_skillset_items(skill_name);
CREATE INDEX IF NOT EXISTS idx_aihub_skillset_items_order
    ON aihub_skillset_items(skillset_name, sort_order, skill_name);

COMMENT ON TABLE aihub_skillsets IS
    'Lightweight collections of Skills; Skill lifecycle and versions remain independent.';
COMMENT ON COLUMN aihub_skillset_items.skill_name IS
    'Reference to a canonical Skill only; no version is persisted by design.';
