CREATE TABLE identities (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('user', 'group')),
    native TEXT NOT NULL,
    CHECK (id > 0 AND id < 900000000000000),
    UNIQUE (namespace, kind, native)
);

CREATE TABLE identity_links (
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL,
    native TEXT NOT NULL,
    real_id INTEGER NOT NULL CHECK (real_id > 0 AND real_id < 100000000000000),
    PRIMARY KEY (namespace, kind, native),
    UNIQUE (namespace, kind, real_id),
    FOREIGN KEY (namespace, kind, native) REFERENCES identities(namespace, kind, native)
);

CREATE TABLE message_refs (
    id INTEGER PRIMARY KEY AUTOINCREMENT CHECK (id BETWEEN 1 AND 2147483647),
    namespace TEXT NOT NULL,
    chat_kind TEXT NOT NULL CHECK (chat_kind IN ('group', 'private')),
    peer TEXT NOT NULL,
    user_native TEXT NOT NULL,
    native_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    reference_id TEXT NOT NULL,
    incoming INTEGER NOT NULL CHECK (incoming IN (0, 1)),
    packet BLOB,
    received_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    UNIQUE (namespace, chat_kind, peer, native_id, incoming)
);
CREATE INDEX message_refs_expiry ON message_refs(expires_at);
CREATE INDEX message_refs_reference ON message_refs(namespace, chat_kind, peer, reference_id);

CREATE TABLE reply_sequences (
    namespace TEXT NOT NULL,
    peer TEXT NOT NULL,
    credential TEXT NOT NULL,
    sequence INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    PRIMARY KEY (namespace, peer, credential)
);

CREATE TABLE evidence (
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('user', 'group')),
    native TEXT NOT NULL,
    real_id INTEGER NOT NULL,
    official_message TEXT NOT NULL,
    observation TEXT NOT NULL,
    sender INTEGER NOT NULL,
    distance INTEGER NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY (namespace, kind, official_message),
    UNIQUE (namespace, kind, observation)
);
CREATE INDEX evidence_candidate ON evidence(namespace, kind, native, real_id, recorded_at);
CREATE INDEX evidence_expiry ON evidence(recorded_at);

CREATE TABLE conflicts (
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL,
    native TEXT NOT NULL,
    real_id INTEGER NOT NULL,
    reason TEXT NOT NULL,
    recorded_at INTEGER NOT NULL,
    PRIMARY KEY (namespace, kind, native, real_id)
);

CREATE TABLE audit (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    at INTEGER NOT NULL,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    namespace TEXT NOT NULL,
    kind TEXT NOT NULL,
    native TEXT NOT NULL,
    before_id INTEGER,
    after_id INTEGER
);
