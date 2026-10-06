-- Holidays: SKB defaults (source 'skb') plus HR additions (source 'hr').
-- Cancelling keeps the row with active = false, so restarts (which re-seed
-- the defaults with ON CONFLICT DO NOTHING) never bring a cancelled day back.
CREATE TABLE IF NOT EXISTS holidays (
    day        DATE PRIMARY KEY,
    name       TEXT NOT NULL,
    kind       TEXT NOT NULL CHECK (kind IN ('nasional', 'cuti_bersama', 'kantor')),
    active     BOOLEAN NOT NULL DEFAULT TRUE,
    source     TEXT NOT NULL DEFAULT 'hr',
    updated_by BIGINT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
