-- SkillSet creation defaults to publicly discoverable: a SkillSet is a
-- lightweight catalog grouping, not an access boundary, and referenced
-- Skills retain their own authorization. Align the column default with
-- the application-layer normalizeVisibility default (see skillset_http.go).

ALTER TABLE aihub_skillsets
    ALTER COLUMN visibility SET DEFAULT 'public';
