-- Fix tool catalog soft-delete uniqueness.
--
-- aihub_tools.tool_id used a plain UNIQUE constraint. Deletes are soft
-- (deleted_at is set, the row stays), so a deleted tool's id kept occupying
-- the unique constraint and re-creating a tool with the same id failed with
-- a unique violation (23505). Converting uniqueness to a PARTIAL unique index
-- that ignores soft-deleted rows lets deleted ids be reused while still
-- enforcing uniqueness among active rows (same fix as 202607290001 for the
-- model catalog).
--
-- aihub_tool_versions.tool_id had an FK reference to aihub_tools(tool_id);
-- Postgres requires the referenced column to carry a full (non-partial)
-- unique constraint, so the FK is dropped first. The cascade only ever fired
-- on hard delete, which this system never performs (deletes are soft), so
-- dropping it changes no runtime behavior; version rows of a soft-deleted
-- tool are intentionally retained for audit.
--
-- Idempotent so it is safe to re-run (e.g. after a manual apply).

ALTER TABLE aihub_tool_versions
    DROP CONSTRAINT IF EXISTS fk_aihub_tool_versions_tool;

ALTER TABLE aihub_tools
    DROP CONSTRAINT IF EXISTS aihub_tools_tool_id_key;

CREATE UNIQUE INDEX IF NOT EXISTS aihub_tools_tool_id_uniq
    ON aihub_tools (tool_id) WHERE deleted_at IS NULL;
