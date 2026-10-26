CREATE TABLE players (
    id uuid PRIMARY KEY,
    name text,
    progress jsonb,
    xp bigint NOT NULL DEFAULT 0 CHECK (xp >= 0),
    revision bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE devices (
    id uuid PRIMARY KEY,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    last_seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE credentials (
    id bytea PRIMARY KEY,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    data jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX credentials_player_id_idx ON credentials(player_id);

CREATE TABLE ceremonies (
    id uuid PRIMARY KEY,
    kind text NOT NULL CHECK (kind IN ('register', 'login')),
    player_id uuid,
    device_id uuid,
    session_data jsonb NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE INDEX ceremonies_expires_at_idx ON ceremonies(expires_at);

CREATE TABLE sessions (
    token_hash bytea PRIMARY KEY,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX sessions_expires_at_idx ON sessions(expires_at);

CREATE TABLE runs (
    id uuid PRIMARY KEY,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    idempotency_key uuid NOT NULL,
    score bigint NOT NULL DEFAULT 0 CHECK (score >= 0),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'finished', 'abandoned')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (player_id, idempotency_key)
);
CREATE INDEX runs_player_id_idx ON runs(player_id);
