-- Fix model management V2 soft-delete uniqueness.
--
-- The original schema used a plain UNIQUE(org_id, code) on aihub_models_v2 and
-- aihub_model_profiles_v2. Deletes are soft deletes (deleted_at is set, the row
-- stays), so a deleted model/profile's code kept occupying the unique
-- constraint. Re-creating a model with the same code then failed with
-- "model resource already exists" (Postgres 23505 -> MODEL_MANAGEMENT_CONFLICT).
--
-- Converting the uniqueness to a PARTIAL unique index that ignores soft-deleted
-- rows lets deleted codes be reused while still enforcing uniqueness among
-- active rows. Idempotent so it is safe to re-run (e.g. after a manual apply).

-- aihub_models_v2
ALTER TABLE aihub_models_v2
    DROP CONSTRAINT IF EXISTS aihub_models_v2_org_id_code_key;
CREATE UNIQUE INDEX IF NOT EXISTS aihub_models_v2_org_code_uniq
    ON aihub_models_v2 (org_id, code) WHERE deleted_at IS NULL;

-- aihub_model_profiles_v2
ALTER TABLE aihub_model_profiles_v2
    DROP CONSTRAINT IF EXISTS aihub_model_profiles_v2_org_id_code_key;
CREATE UNIQUE INDEX IF NOT EXISTS aihub_model_profiles_v2_org_code_uniq
    ON aihub_model_profiles_v2 (org_id, code) WHERE deleted_at IS NULL;
