CREATE TABLE lan_sessions (
    id uuid PRIMARY KEY,
    token_hash bytea NOT NULL UNIQUE,
    public_ip inet NOT NULL,
    name text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    active_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '30 minutes'
);
CREATE INDEX lan_sessions_public_ip_active_idx ON lan_sessions(public_ip, active_at);

CREATE TABLE lan_invites (
    id uuid PRIMARY KEY,
    host_id uuid NOT NULL REFERENCES lan_sessions(id) ON DELETE CASCADE,
    guest_id uuid NOT NULL REFERENCES lan_sessions(id) ON DELETE CASCADE,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'declined', 'cancelled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL DEFAULT now() + interval '2 minutes',
    CHECK (host_id <> guest_id)
);
CREATE INDEX lan_invites_host_idx ON lan_invites(host_id, expires_at);
CREATE INDEX lan_invites_guest_idx ON lan_invites(guest_id, expires_at);

CREATE TABLE lan_signals (
    id bigserial PRIMARY KEY,
    invite_id uuid NOT NULL REFERENCES lan_invites(id) ON DELETE CASCADE,
    sender_id uuid NOT NULL REFERENCES lan_sessions(id) ON DELETE CASCADE,
    kind text NOT NULL CHECK (kind IN ('offer', 'answer')),
    signal_id uuid NOT NULL,
    room_code text NOT NULL,
    sdp text NOT NULL,
    wifi_address text,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (invite_id, sender_id, kind)
);
