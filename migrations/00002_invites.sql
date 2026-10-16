-- +goose Up
-- Приглашение в семью: одноразовая ссылка t.me/<бот>?start=<code>, которую глава создаёт в панели.
CREATE TABLE invites (
    code       TEXT PRIMARY KEY,
    family_id  BIGINT NOT NULL REFERENCES families (id),
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL,
    used_by    BIGINT,
    used_at    TIMESTAMPTZ
);

-- Кто вошёл по приглашению: так бот пускает их, когда FAMILY_MEMBERS задан.
CREATE INDEX invites_used_by_idx ON invites (used_by) WHERE used_by IS NOT NULL;

-- +goose Down
DROP TABLE invites;
