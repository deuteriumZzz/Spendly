-- +goose Up
CREATE TABLE families (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Семья по умолчанию (id = 1): пока сервис обслуживает одну семью.
INSERT INTO families (name) VALUES ('Семья');

CREATE TABLE users (
    telegram_id BIGINT PRIMARY KEY,
    family_id   BIGINT NOT NULL REFERENCES families (id),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (telegram_id, family_id)
);

CREATE TABLE expenses (
    id         BIGSERIAL PRIMARY KEY,
    family_id  BIGINT NOT NULL,
    user_id    BIGINT NOT NULL,
    amount     NUMERIC(12, 2) NOT NULL CHECK (amount > 0),
    category   TEXT NOT NULL CHECK (category IN
               ('еда', 'транспорт', 'жильё', 'развлечения', 'здоровье', 'покупки', 'другое')),
    spent_at   DATE NOT NULL,
    comment    TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- трата всегда принадлежит той же семье, что и её владелец
    FOREIGN KEY (user_id, family_id) REFERENCES users (telegram_id, family_id) ON UPDATE CASCADE
);

CREATE INDEX expenses_family_spent_idx ON expenses (family_id, spent_at DESC);
CREATE INDEX expenses_user_spent_idx ON expenses (user_id, spent_at DESC);

-- История диалога с ботом. update_id защищает от повторной обработки одного сообщения Telegram.
CREATE TABLE messages (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (telegram_id),
    update_id  BIGINT UNIQUE,
    role       TEXT NOT NULL CHECK (role IN ('user', 'assistant')),
    content    TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX messages_user_created_idx ON messages (user_id, created_at DESC);

-- +goose Down
DROP TABLE messages;
DROP TABLE expenses;
DROP TABLE users;
DROP TABLE families;
