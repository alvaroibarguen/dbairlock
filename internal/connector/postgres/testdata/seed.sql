-- Seed fixture for the connector conformance suite (see
-- internal/connector/connectortest). Run as a superuser in a fresh database.

-- Postgres before 15 lets PUBLIC create in schema public; the
-- least-privilege user must not be able to.
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
    user_id integer NOT NULL,
    total   numeric(10,2) NOT NULL,
    status  varchar(20) NOT NULL
);
INSERT INTO orders (id, user_id, total, status) VALUES
    (1, 1, 10.00, 'paid'),
    (2, 1, 20.00, 'open'),
    (3, 2, 150.00, 'open'),
    (4, 4, 5.00, 'open');

-- No primary key on purpose, and two identical rows.
CREATE TABLE audit_notes (
    user_id integer NOT NULL,
    note    text
);
INSERT INTO audit_notes (user_id, note) VALUES
    (1, 'created'),
    (1, 'created'),
    (2, 'verified');

-- One row of awkward values for cell conversion.
CREATE TABLE cell_values (
    id       integer PRIMARY KEY,
    note     text,
    n_null   text,
    big      text,
    bin      bytea,
    doc      jsonb,
    ts       timestamptz,
    amount   numeric(20,6),
    tags     text[],
    nums     integer[],
    flag     boolean,
    big_int  bigint,
    f        double precision,
    d        date,
    plain_ts timestamp
);
INSERT INTO cell_values VALUES (
    1, 'n', NULL, repeat('x', 100), '\x00ff10'::bytea,
    '{"a": 1, "b": [true, null, "s"]}', '2024-03-04 05:06:07.123456+02',
    12345.678900, ARRAY['a', 'b c', NULL, 'q"x'], ARRAY[1, 2, 3], true,
    9007199254740993, 1.5, '2024-01-02', '2024-01-02 03:04:05'
);
