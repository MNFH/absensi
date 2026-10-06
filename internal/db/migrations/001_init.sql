-- phone: normalised digits, e.g. 6281234567890 (HR's source of truth).
-- telegram_id: NULL until the worker proves ownership of the phone by sharing
-- their own Telegram contact; then locked until an admin runs /unbind.
CREATE TABLE IF NOT EXISTS users (
    id          BIGSERIAL PRIMARY KEY,
    phone       TEXT UNIQUE,
    telegram_id BIGINT UNIQUE,
    name        TEXT NOT NULL,
    role        TEXT NOT NULL DEFAULT 'worker' CHECK (role IN ('admin', 'worker')),
    is_active   BOOLEAN NOT NULL DEFAULT TRUE,
    created_by  BIGINT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (phone IS NOT NULL OR telegram_id IS NOT NULL)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id         BIGSERIAL PRIMARY KEY,
    actor      TEXT NOT NULL,   -- telegram id of the actor, or 'system'
    action     TEXT NOT NULL,
    target     TEXT,            -- phone or telegram id of the affected user
    detail     JSONB NOT NULL DEFAULT '{}',
    at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS audit_log_at_idx ON audit_log (at DESC);
