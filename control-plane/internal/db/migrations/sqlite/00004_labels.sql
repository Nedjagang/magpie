-- +goose Up
CREATE TABLE agent_labels (
    instance_uid TEXT PRIMARY KEY,
    product      TEXT NOT NULL,
    variant      TEXT NOT NULL,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- +goose Down
DROP TABLE agent_labels;
