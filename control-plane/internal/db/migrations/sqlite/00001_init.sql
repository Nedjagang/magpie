-- +goose Up
CREATE TABLE configs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT NOT NULL,
    yaml       TEXT NOT NULL,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_configs_name ON configs(name);

-- +goose Down
DROP TABLE configs;
