-- Add created_by_name to hub_skill_profiles so the skill detail panel can show
-- a human-readable owner name (Casdoor displayName) without a cross-service
-- IAM lookup. Populated at skill creation from the authenticated principal's
-- Name claim; existing rows keep the default empty string and clients fall
-- back to owner_id.

ALTER TABLE hub_skill_profiles
    ADD COLUMN IF NOT EXISTS created_by_name VARCHAR(256) NOT NULL DEFAULT '';
