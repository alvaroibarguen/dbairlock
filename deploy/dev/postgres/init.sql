-- Sample Postgres target. Runs once, as the postgres superuser, in database
-- "app" when the container is first created. The tables match the
-- connector conformance suite's seed fixture.

CREATE ROLE dbairlock_app LOGIN PASSWORD 'app-secret';
CREATE ROLE dbairlock_super LOGIN SUPERUSER PASSWORD 'super-secret';

-- Postgres before 15 lets PUBLIC create in schema public.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;

CREATE TABLE users (
    id         integer PRIMARY KEY,
    email      varchar(200) NOT NULL,
    name       varchar(200),
    status     varchar(20) NOT NULL,
    updated_at timestamp
);
INSERT INTO users (id, email, name, status) VALUES
    (1, 'ann@example.com', 'Ann', 'active'),
    (2, 'bob@example.com', 'Bob', 'active'),
    (3, 'cat@example.com', 'Cat', 'banned'),
    (4, 'dan@example.com', 'Dan', 'active'),
    (5, 'eve@example.com', 'Eve', 'inactive');

CREATE TABLE orders (
    id      integer PRIMARY KEY,
    user_id integer NOT NULL REFERENCES users (id),
    total   numeric(10,2) NOT NULL,
    status  varchar(20) NOT NULL
);
INSERT INTO orders (id, user_id, total, status) VALUES
    (1, 1, 10.00, 'paid'),
    (2, 1, 20.00, 'open'),
    (3, 2, 150.00, 'open'),
    (4, 4, 5.00, 'open');

-- No primary key on purpose: execution compares whole rows.
CREATE TABLE audit_notes (
    user_id integer NOT NULL,
    note    text
);
INSERT INTO audit_notes (user_id, note) VALUES
    (1, 'created'),
    (1, 'created'),
    (2, 'verified');

GRANT USAGE ON SCHEMA public TO dbairlock_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON users, orders, audit_notes TO dbairlock_app;
