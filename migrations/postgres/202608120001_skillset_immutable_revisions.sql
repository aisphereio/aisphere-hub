-- Persist immutable SkillSet revisions.
--
-- aihub_skillsets + aihub_skillset_items remain the mutable catalog projection
-- used by the existing CRUD API. Every committed revision is copied into the
-- append-only tables below so AgentDefinition can pin SkillSet@revision and
-- reproduce the exact member set later.

CREATE TABLE IF NOT EXISTS aihub_skillset_revisions (
    skillset_name VARCHAR(128) NOT NULL,
    revision      BIGINT NOT NULL,
    display_name  VARCHAR(256) NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    visibility    VARCHAR(32) NOT NULL DEFAULT 'private',
    owner_id      VARCHAR(128) NOT NULL DEFAULT '',
    org_id        VARCHAR(128) NOT NULL DEFAULT '',
    captured_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (skillset_name, revision),
    CONSTRAINT chk_aihub_skillset_revisions_visibility
        CHECK (visibility IN ('private', 'internal', 'public'))
);

CREATE TABLE IF NOT EXISTS aihub_skillset_revision_items (
    skillset_name   VARCHAR(128) NOT NULL,
    revision        BIGINT NOT NULL,
    skill_name      VARCHAR(128) NOT NULL,
    sort_order      INTEGER NOT NULL DEFAULT 0,
    version         VARCHAR(128) NOT NULL,
    commit_sha      VARCHAR(64) NOT NULL,
    tree_sha        VARCHAR(64) NOT NULL,
    manifest_sha256 VARCHAR(64) NOT NULL,
    resolved_at     TIMESTAMPTZ NULL,
    PRIMARY KEY (skillset_name, revision, skill_name),
    CONSTRAINT fk_aihub_skillset_revision_items_revision
        FOREIGN KEY (skillset_name, revision)
        REFERENCES aihub_skillset_revisions(skillset_name, revision)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_aihub_skillset_revision_items_skill
    ON aihub_skillset_revision_items(skill_name, version);

-- Snapshot helper is deliberately insert-only. Once (name, revision) exists,
-- neither metadata nor member rows are overwritten. This makes a revision a
-- durable execution input rather than a label pointing at mutable rows.
CREATE OR REPLACE FUNCTION aihub_snapshot_skillset_revision(
    p_skillset_name VARCHAR,
    p_revision BIGINT
) RETURNS VOID AS $$
BEGIN
    INSERT INTO aihub_skillset_revisions(
        skillset_name, revision, display_name, description, visibility,
        owner_id, org_id, captured_at
    )
    SELECT name, revision, display_name, description, visibility,
           owner_id, org_id, NOW()
      FROM aihub_skillsets
     WHERE name = p_skillset_name
       AND revision = p_revision
    ON CONFLICT (skillset_name, revision) DO NOTHING;

    -- Preserve every member row, including legacy rows that predate exact
    -- release pinning. An incomplete historical row is still evidence of what
    -- existed at that revision; the resolver rejects it explicitly instead of
    -- silently dropping it and producing a different member set.
    INSERT INTO aihub_skillset_revision_items(
        skillset_name, revision, skill_name, sort_order, version,
        commit_sha, tree_sha, manifest_sha256, resolved_at
    )
    SELECT i.skillset_name, p_revision, i.skill_name, i.sort_order, i.version,
           i.commit_sha, i.tree_sha, i.manifest_sha256, i.resolved_at
      FROM aihub_skillset_items i
     WHERE i.skillset_name = p_skillset_name
    ON CONFLICT (skillset_name, revision, skill_name) DO NOTHING;
END;
$$ LANGUAGE plpgsql;

-- Existing rows get one immutable baseline at their current revision.
SELECT aihub_snapshot_skillset_revision(name, revision)
  FROM aihub_skillsets
 WHERE deleted_at IS NULL;

-- Existing mutation endpoints update members first and increment revision as
-- the final statement of the same transaction. An AFTER UPDATE trigger thus
-- observes the complete new member set and freezes it atomically.
CREATE OR REPLACE FUNCTION aihub_skillset_revision_updated() RETURNS TRIGGER AS $$
BEGIN
    IF NEW.revision IS DISTINCT FROM OLD.revision THEN
        PERFORM aihub_snapshot_skillset_revision(NEW.name, NEW.revision);
    END IF;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_aihub_skillset_revision_updated ON aihub_skillsets;
CREATE TRIGGER trg_aihub_skillset_revision_updated
AFTER UPDATE OF revision ON aihub_skillsets
FOR EACH ROW
EXECUTE FUNCTION aihub_skillset_revision_updated();

-- CreateSkillSet inserts the set before its member rows. Defer the initial
-- snapshot until transaction end so revision=1 contains the complete members.
CREATE OR REPLACE FUNCTION aihub_skillset_initial_revision() RETURNS TRIGGER AS $$
BEGIN
    PERFORM aihub_snapshot_skillset_revision(NEW.name, NEW.revision);
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

DROP TRIGGER IF EXISTS trg_aihub_skillset_initial_revision ON aihub_skillsets;
CREATE CONSTRAINT TRIGGER trg_aihub_skillset_initial_revision
AFTER INSERT ON aihub_skillsets
DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW
EXECUTE FUNCTION aihub_skillset_initial_revision();

COMMENT ON TABLE aihub_skillset_revisions IS
    'Append-only SkillSet metadata snapshots addressable by exact revision.';
COMMENT ON TABLE aihub_skillset_revision_items IS
    'Append-only exact Skill release membership for a SkillSet revision.';
