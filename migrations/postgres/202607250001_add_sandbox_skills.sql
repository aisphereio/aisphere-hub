-- Add skill declarations to sandbox templates and sandboxes.
--
-- A SandboxTemplate declares which published skill releases sandboxes created
-- from it should install. A Sandbox carries the resolved snapshot (template
-- skills + inline CreateSandboxRequest skills, merged). The resolved list is
-- written to the Sandbox CRD metadata annotation aisphere.io/skills at apply
-- time; the future AISphere Runtime / sandbox sidecar reads that annotation at
-- pod boot and fetches each release via the skillx SDK.
--
-- Hub only carries the declaration (control plane); it does not fetch skills
-- (design §2.2: Hub = control plane, Runtime = execution plane). The column is
-- a JSONB array of {"name","version"} objects, defaulting to an empty array.

ALTER TABLE k8s_sandbox_templates
    ADD COLUMN IF NOT EXISTS skills_json JSONB NOT NULL DEFAULT '[]'::jsonb;

ALTER TABLE k8s_sandboxes
    ADD COLUMN IF NOT EXISTS skills_json JSONB NOT NULL DEFAULT '[]'::jsonb;

-- +goose StatementBegin
COMMENT ON COLUMN k8s_sandbox_templates.skills_json IS
    'JSONB array of {"name","version"} skill declarations; inherited by sandboxes created from this template.';
COMMENT ON COLUMN k8s_sandboxes.skills_json IS
    'JSONB array of {"name","version"} resolved skill declarations (template + inline); also written to the Sandbox CRD annotation aisphere.io/skills for the Runtime.';
-- +goose StatementEnd
