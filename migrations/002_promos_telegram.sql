CREATE TABLE promo_codes (
    id uuid PRIMARY KEY,
    code_hash bytea NOT NULL UNIQUE,
    coins bigint NOT NULL CHECK (coins > 0 AND coins <= 9007199254740991),
    max_redemptions integer NOT NULL CHECK (max_redemptions > 0),
    redeemed_count integer NOT NULL DEFAULT 0 CHECK (redeemed_count >= 0),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE promo_redemptions (
    code_id uuid NOT NULL REFERENCES promo_codes(id) ON DELETE CASCADE,
    player_id uuid NOT NULL REFERENCES players(id) ON DELETE CASCADE,
    redeemed_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (code_id, player_id)
);

CREATE TABLE telegram_link_challenges (
    player_id uuid PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    code_hash bytea NOT NULL UNIQUE,
    expires_at timestamptz NOT NULL
);
CREATE INDEX telegram_link_challenges_expires_at_idx ON telegram_link_challenges(expires_at);

CREATE TABLE telegram_accounts (
    player_id uuid PRIMARY KEY REFERENCES players(id) ON DELETE CASCADE,
    telegram_id bigint NOT NULL UNIQUE,
    chat_id bigint NOT NULL,
    linked_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE telegram_updates (
    update_id bigint PRIMARY KEY,
    chat_id bigint NOT NULL,
    response text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
