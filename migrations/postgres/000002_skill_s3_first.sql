-- 000002_skill_s3_first.sql
-- Move skill package storage to S3-first semantics.
-- PG keeps control metadata only: package key in revision and manifest/package
-- pointers in manifest_json. Existing aihub_skill_files rows can remain for
-- old data, but new writes no longer insert file content rows.

ALTER TABLE IF EXISTS aihub_skill_versions
  ALTER COLUMN revision TYPE TEXT;
