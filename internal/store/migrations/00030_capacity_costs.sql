-- +goose Up
-- V3 M24: capacity automation and cost attribution (V3 §5).

-- Where a node came from and what it costs (§5.1, §5.4). provider is
-- 'manual' for machines registered by hand (the install's own node, colo
-- servers) and 'hetzner' for servers PGDock created. region groups nodes
-- until M25's region model: one name per installation by default.
-- lifecycle: 'provisioning' while a created server's agent hasn't joined,
-- 'draining' while its projects are being moved off (nothing new is
-- placed there), else 'active'. empty_since is when a node last became
-- empty of instances; empty nodes are deleted after 24 hours (§5.3).
ALTER TABLE nodes
  ADD COLUMN provider            text NOT NULL DEFAULT 'manual',
  ADD COLUMN region              text NOT NULL DEFAULT 'eu-central',
  ADD COLUMN server_type         text,
  ADD COLUMN monthly_cost_minor  bigint,
  ADD COLUMN cost_currency       text NOT NULL DEFAULT 'EUR',
  ADD COLUMN lifecycle           text NOT NULL DEFAULT 'active'
    CHECK (lifecycle IN ('active', 'provisioning', 'draining')),
  ADD COLUMN empty_since         timestamptz,
  ADD COLUMN keep                boolean NOT NULL DEFAULT false;
CREATE INDEX nodes_region ON nodes (region) WHERE status <> 'removed';

-- Capacity proposals (§5.2): a threshold tripped, so PGDock proposes a
-- server. Within the monthly infrastructure budget it is applied by
-- itself; above it, it waits for the platform admin.
CREATE TABLE capacity_proposals (
  id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  region             text NOT NULL,
  tier               text NOT NULL CHECK (tier IN ('shared', 'dedicated')),
  reason             text NOT NULL,
  provider           text NOT NULL,
  server_type        text NOT NULL,
  location           text NOT NULL DEFAULT '',
  monthly_cost_minor bigint NOT NULL,
  currency           text NOT NULL,
  status             text NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'approved', 'provisioning', 'done', 'rejected', 'failed', 'superseded')),
  auto               boolean NOT NULL DEFAULT false,
  node_id            uuid REFERENCES nodes(id),
  operation_id       uuid,
  error              text,
  decided_by         uuid REFERENCES users(id),
  decided_at         timestamptz,
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now()
);
-- One open proposal per region and tier: a sweep that trips again while
-- one waits doesn't add another.
CREATE UNIQUE INDEX capacity_proposals_open ON capacity_proposals (region, tier)
  WHERE status IN ('pending', 'approved', 'provisioning');

-- Rebalancing and drains (§5.3): the moves PGDock proposes, batched. A
-- drain's moves are approved when the drain starts; a rebalance batch
-- waits for the admin unless automatic rebalancing is on.
CREATE TABLE rebalance_moves (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  batch        uuid NOT NULL,
  kind         text NOT NULL CHECK (kind IN ('drain', 'rebalance')),
  project_id   uuid NOT NULL REFERENCES projects(id),
  from_node    uuid NOT NULL REFERENCES nodes(id),
  to_node      uuid REFERENCES nodes(id),
  reason       text NOT NULL,
  status       text NOT NULL DEFAULT 'proposed'
    CHECK (status IN ('proposed', 'approved', 'moving', 'done', 'failed', 'skipped', 'rejected')),
  operation_id uuid,
  error        text,
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX rebalance_moves_open ON rebalance_moves (status, created_at) WHERE status IN ('proposed', 'approved', 'moving');
CREATE UNIQUE INDEX rebalance_moves_one_open ON rebalance_moves (project_id) WHERE status IN ('proposed', 'approved', 'moving');

-- Exchange rates (§7.2 FX view): naira per unit of a currency, as of a
-- time. The latest is "the current rate".
CREATE TABLE fx_rates (
  id           bigserial PRIMARY KEY,
  currency     text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
  ngn_per_unit numeric NOT NULL CHECK (ngn_per_unit > 0),
  effective_at timestamptz NOT NULL DEFAULT now(),
  source       text NOT NULL DEFAULT 'manual',
  set_by       uuid REFERENCES users(id),
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fx_rates_latest ON fx_rates (currency, effective_at DESC);

-- Daily cost attribution (§5.4). amount is in minor units of currency
-- (cents for EUR), kept fractional so a month adds up exactly. org_id is
-- the nil UUID for costs no organisation is charged with: idle capacity
-- and fixed overheads. category: shared, dedicated, ha, backup, egress,
-- floating_ip, overhead, idle.
CREATE TABLE cost_allocations (
  day          date NOT NULL,
  org_id       uuid NOT NULL,
  category     text NOT NULL,
  region       text NOT NULL,
  currency     text NOT NULL,
  amount_minor numeric NOT NULL,
  quantity     numeric NOT NULL DEFAULT 0,
  PRIMARY KEY (day, org_id, category, region, currency)
);
CREATE INDEX cost_allocations_org ON cost_allocations (org_id, day);

-- +goose Down
DROP TABLE cost_allocations;
DROP TABLE fx_rates;
DROP TABLE rebalance_moves;
DROP TABLE capacity_proposals;
DROP INDEX nodes_region;
ALTER TABLE nodes DROP COLUMN keep, DROP COLUMN empty_since, DROP COLUMN lifecycle, DROP COLUMN cost_currency,
  DROP COLUMN monthly_cost_minor, DROP COLUMN server_type, DROP COLUMN region, DROP COLUMN provider;
