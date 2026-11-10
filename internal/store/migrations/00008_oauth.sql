-- OAuth sign-in for the MCP endpoint: dbairlock is the authorization server
-- for its own /mcp resource. Conventions as in 00001. Codes and tokens are
-- stored as SHA-256 hashes only, like sessions and API tokens.

-- +goose Up

-- Clients registered through dynamic client registration (RFC 7591). Public
-- clients only (no secret); redirect_uris is a JSON array of loopback URIs.
-- A client that never led to an approved grant is deleted a day after it
-- registered (last_used_at stays NULL until then).
CREATE TABLE oauth_clients (
    id            TEXT    PRIMARY KEY CHECK (id <> ''),
    name          TEXT    NOT NULL CHECK (name <> ''),
    redirect_uris TEXT    NOT NULL CHECK (json_valid(redirect_uris) AND json_type(redirect_uris) = 'array'),
    created_at    INTEGER NOT NULL CHECK (created_at >= 0),
    created_ip    TEXT    NOT NULL DEFAULT '',
    last_used_at  INTEGER CHECK (last_used_at IS NULL OR last_used_at >= 0)
) STRICT;

CREATE INDEX oauth_clients_unused_idx ON oauth_clients (created_at) WHERE last_used_at IS NULL;

-- One person's consent to one client: the "connected app". Tokens belong to
-- a grant; revoking the grant ends all of them. client_name is copied at
-- approval, so the name a person approved is the name shown afterwards.
CREATE TABLE oauth_grants (
    id             TEXT    PRIMARY KEY CHECK (id <> ''),
    client_id      TEXT    NOT NULL REFERENCES oauth_clients (id),
    user_id        TEXT    NOT NULL REFERENCES users (id),
    client_name    TEXT    NOT NULL CHECK (client_name <> ''),
    redirect_uri   TEXT    NOT NULL CHECK (redirect_uri <> ''),
    resource       TEXT    NOT NULL CHECK (resource <> ''),
    scope          TEXT    NOT NULL,
    created_at     INTEGER NOT NULL CHECK (created_at >= 0),
    last_used_at   INTEGER CHECK (last_used_at IS NULL OR last_used_at >= 0),
    revoked_at     INTEGER CHECK (revoked_at IS NULL OR revoked_at >= 0),
    revoked_reason TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX oauth_grants_user_idx ON oauth_grants (user_id, created_at);
CREATE INDEX oauth_grants_client_idx ON oauth_grants (client_id);

-- Authorization codes: single use, short-lived, bound to the grant (client,
-- user, resource), the redirect URI and the PKCE challenge.
CREATE TABLE oauth_codes (
    code_hash      BLOB    PRIMARY KEY CHECK (length(code_hash) = 32),
    grant_id       TEXT    NOT NULL REFERENCES oauth_grants (id),
    redirect_uri   TEXT    NOT NULL CHECK (redirect_uri <> ''),
    code_challenge TEXT    NOT NULL CHECK (length(code_challenge) = 43),
    created_at     INTEGER NOT NULL CHECK (created_at >= 0),
    expires_at     INTEGER NOT NULL CHECK (expires_at >= created_at),
    used_at        INTEGER CHECK (used_at IS NULL OR used_at >= 0)
) STRICT;

CREATE INDEX oauth_codes_grant_idx ON oauth_codes (grant_id);

-- Access and refresh tokens. A refresh token is used once: used_at is set
-- when it is exchanged, and presenting it again revokes the grant.
CREATE TABLE oauth_tokens (
    token_hash BLOB    PRIMARY KEY CHECK (length(token_hash) = 32),
    grant_id   TEXT    NOT NULL REFERENCES oauth_grants (id),
    kind       TEXT    NOT NULL CHECK (kind IN ('access', 'refresh')),
    created_at INTEGER NOT NULL CHECK (created_at >= 0),
    expires_at INTEGER NOT NULL CHECK (expires_at >= created_at),
    used_at    INTEGER CHECK (used_at IS NULL OR used_at >= 0)
) STRICT;

CREATE INDEX oauth_tokens_grant_idx ON oauth_tokens (grant_id);
CREATE INDEX oauth_tokens_expiry_idx ON oauth_tokens (expires_at);

-- +goose Down
DROP TABLE oauth_tokens;
DROP TABLE oauth_codes;
DROP TABLE oauth_grants;
DROP TABLE oauth_clients;
