-- +goose Up
ALTER TABLE transactions ADD COLUMN request_hash TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE transactions DROP COLUMN request_hash;
