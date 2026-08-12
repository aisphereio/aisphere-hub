-- Align the persisted Skill lifecycle constraint with the public management
-- contract implemented by SkillUsecase: active, disabled and archived are
-- user-facing states, while provisioning, failed and deleting remain internal
-- Saga states.

ALTER TABLE hub_skill_profiles
    DROP CONSTRAINT IF EXISTS chk_hub_skill_profiles_status;

ALTER TABLE hub_skill_profiles
    ADD CONSTRAINT chk_hub_skill_profiles_status
    CHECK (lifecycle_status IN (
        'provisioning',
        'active',
        'disabled',
        'archived',
        'failed',
        'deleting'
    ));

COMMENT ON COLUMN hub_skill_profiles.lifecycle_status IS
    'Skill lifecycle: active/disabled/archived management states plus provisioning/failed/deleting Saga states.';
