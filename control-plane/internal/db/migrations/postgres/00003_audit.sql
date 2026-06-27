-- +goose Up
CREATE TABLE audit_log (
    id         BIGSERIAL PRIMARY KEY,
    at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    actor      TEXT NOT NULL,
    action     TEXT NOT NULL,
    product    TEXT NOT NULL DEFAULT '',
    variant    TEXT NOT NULL DEFAULT '',
    target_id  BIGINT,
    detail     TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_at ON audit_log(at DESC);
CREATE INDEX idx_audit_product ON audit_log(product, variant, at DESC);

-- +goose Down
DROP TABLE audit_log;
