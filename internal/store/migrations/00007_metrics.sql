-- +goose Up
-- Downsampling and pruning scan by resolution and time (spec §8.7).
CREATE INDEX metric_points_resolution_ts_idx ON metric_points (resolution, ts);

-- +goose Down
DROP INDEX metric_points_resolution_ts_idx;
