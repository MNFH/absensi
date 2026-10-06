-- Groups where HR scheduled automatic attendance reminders.
-- in_time / out_time are local "HH:MM" (NULL = off); last_*_sent prevents
-- sending twice on the same day, also across restarts.
CREATE TABLE IF NOT EXISTS reminder_groups (
    chat_id       BIGINT PRIMARY KEY,
    title         TEXT NOT NULL DEFAULT '',
    in_time       TEXT,
    out_time      TEXT,
    last_in_sent  DATE,
    last_out_sent DATE,
    updated_by    BIGINT,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
