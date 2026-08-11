-- 000001_init.up.sql
-- Initial schema: documents + line_items, plus the two enums they
-- rely on and a shared updated_at trigger.
--
-- Money columns are NUMERIC(12, 2) so shopspring/decimal round-trips
-- exactly. Tax percent is (5, 2) — enough for 100.00% with headroom.

CREATE TYPE document_status AS ENUM ('draft', 'finalized');
CREATE TYPE discount_kind AS ENUM ('fixed', 'percent');

CREATE TABLE documents (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id         UUID NOT NULL,
  title           VARCHAR(200) NOT NULL,
  customer        VARCHAR(200) NOT NULL,
  issue_date      DATE NOT NULL,
  status          document_status NOT NULL DEFAULT 'draft',
  subtotal        NUMERIC(12, 2) NOT NULL DEFAULT 0,
  total_discount  NUMERIC(12, 2) NOT NULL DEFAULT 0,
  total_tax       NUMERIC(12, 2) NOT NULL DEFAULT 0,
  grand_total     NUMERIC(12, 2) NOT NULL DEFAULT 0,
  finalized_at    TIMESTAMPTZ,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX documents_user_issue_date_idx ON documents (user_id, issue_date DESC);
CREATE INDEX documents_user_status_idx    ON documents (user_id, status);

CREATE TABLE line_items (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  document_id       UUID NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
  position          INTEGER NOT NULL,
  description       VARCHAR(500) NOT NULL,
  quantity          INTEGER NOT NULL,
  unit_price        NUMERIC(12, 2) NOT NULL,
  discount_type     discount_kind,
  discount_value    NUMERIC(12, 2),
  tax_percent       NUMERIC(5, 2),
  line_subtotal     NUMERIC(12, 2) NOT NULL DEFAULT 0,
  discount_amount   NUMERIC(12, 2) NOT NULL DEFAULT 0,
  after_discount    NUMERIC(12, 2) NOT NULL DEFAULT 0,
  tax_amount        NUMERIC(12, 2) NOT NULL DEFAULT 0,
  line_total        NUMERIC(12, 2) NOT NULL DEFAULT 0,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX line_items_document_position_idx ON line_items (document_id, position);

-- Shared trigger so any UPDATE bumps updated_at without every write
-- path having to remember to set it.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER documents_updated_at BEFORE UPDATE ON documents
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER line_items_updated_at BEFORE UPDATE ON line_items
  FOR EACH ROW EXECUTE FUNCTION set_updated_at();
