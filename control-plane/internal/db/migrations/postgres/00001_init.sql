-- +goose Up
CREATE TABLE configs (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    yaml       TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_configs_name ON configs(name);

-- +goose Down
DROP TABLE configs;
