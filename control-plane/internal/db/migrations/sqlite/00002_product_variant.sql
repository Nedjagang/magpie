-- +goose Up
ALTER TABLE configs ADD COLUMN product TEXT NOT NULL DEFAULT 'default';
ALTER TABLE configs ADD COLUMN variant TEXT NOT NULL DEFAULT 'default';

CREATE INDEX idx_configs_product_variant ON configs(product, variant, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_configs_product_variant;
ALTER TABLE configs DROP COLUMN variant;
ALTER TABLE configs DROP COLUMN product;
