-- Seed fixture for the connector conformance suite (see
-- internal/connector/connectortest). Run as root in a fresh database.

CREATE TABLE users (
    id         int PRIMARY KEY,
    email      varchar(200) NOT NULL,
    name       varchar(200),
    status     varchar(20) NOT NULL,
    updated_at datetime(6)
) ENGINE = InnoDB;
INSERT INTO users (id, email, name, status) VALUES
    (1, 'ann@example.com', 'Ann', 'active'),
    (2, 'bob@example.com', 'Bob', 'active'),
    (3, 'cat@example.com', 'Cat', 'banned'),
    (4, 'dan@example.com', 'Dan', 'active'),
    (5, 'eve@example.com', 'Eve', 'inactive');

CREATE TABLE orders (
    id      int PRIMARY KEY,
    user_id int NOT NULL,
    total   decimal(10,2) NOT NULL,
    status  varchar(20) NOT NULL
) ENGINE = InnoDB;
INSERT INTO orders (id, user_id, total, status) VALUES
    (1, 1, 10.00, 'paid'),
    (2, 1, 20.00, 'open'),
    (3, 2, 150.00, 'open'),
    (4, 4, 5.00, 'open');

-- No primary key on purpose, and two identical rows.
CREATE TABLE audit_notes (
    user_id int NOT NULL,
    note    text
) ENGINE = InnoDB;
INSERT INTO audit_notes (user_id, note) VALUES
    (1, 'created'),
    (1, 'created'),
    (2, 'verified');

-- One row of awkward values for cell conversion.
CREATE TABLE cell_values (
    id       int PRIMARY KEY,
    note     text,
    n_null   text,
    big      text,
    bin      varbinary(16),
    doc      json,
    ts       timestamp(6) NULL,
    amount   decimal(20,6),
    flag     boolean,
    big_int  bigint,
    uns      bigint unsigned,
    f        double,
    d        date,
    plain_ts datetime,
    tm       time,
    yr       year,
    bits     bit(8),
    kind     enum('a', 'b c'),
    tags     set('x', 'y'),
    uni      varchar(20)
) ENGINE = InnoDB;
INSERT INTO cell_values VALUES (
    1, 'n', NULL, REPEAT('x', 100), 0x00FF10,
    '{"b": [true, null, "s"], "a": 1}', '2024-03-04 05:06:07.123456+02:00',
    12345.678900, TRUE, 9007199254740993, 18446744073709551615, 1.5,
    '2024-01-02', '2024-01-02 03:04:05', '12:34:56', 2024, b'101', 'b c', 'x,y',
    'ñandú €'
);
