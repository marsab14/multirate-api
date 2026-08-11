-- 000001_init.down.sql
-- Reverses 000001_init.up.sql. line_items is dropped first so the
-- FK to documents is gone before documents itself is removed.

DROP TABLE IF EXISTS line_items;
DROP TABLE IF EXISTS documents;
DROP FUNCTION IF EXISTS set_updated_at;
DROP TYPE IF EXISTS discount_kind;
DROP TYPE IF EXISTS document_status;
