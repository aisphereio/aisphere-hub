-- Fix the k8s_sandboxes.operating_mode check constraint to match the
-- agent-sandbox Sandbox CRD spec.operatingMode enum, which uses Title case
-- ("Running"/"Suspended"), not all-caps. The biz constants
-- (SandboxOperatingModeRunning="Running") and the provider both carry Title
-- case to mirror the CRD, so the DB constraint must agree — otherwise
-- CreateSandbox violates chk_k8s_sandboxes_operating (SQLSTATE 23514).
--
-- The original 202607230001 migration shipped the constraint in all-caps
-- ('RUNNING','SUSPENDED'); this migration realigns it. Idempotent: re-running
-- on a DB already in Title case is a no-op (drop + re-add the same constraint).
-- Existing rows are normalized first so the re-added constraint is satisfiable.

ALTER TABLE k8s_sandboxes DROP CONSTRAINT IF EXISTS chk_k8s_sandboxes_operating;
ALTER TABLE k8s_sandboxes ALTER COLUMN operating_mode SET DEFAULT 'Running';
UPDATE k8s_sandboxes SET operating_mode = 'Running' WHERE operating_mode = 'RUNNING';
UPDATE k8s_sandboxes SET operating_mode = 'Suspended' WHERE operating_mode = 'SUSPENDED';
ALTER TABLE k8s_sandboxes
    ADD CONSTRAINT chk_k8s_sandboxes_operating
    CHECK (operating_mode IN ('Running','Suspended'));
