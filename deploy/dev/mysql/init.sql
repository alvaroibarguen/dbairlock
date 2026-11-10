-- Sample MySQL target. Runs once, as root, when the container is first
-- created. The tables match the connector conformance suite's seed fixture,
-- plus one MyISAM table for the non-transactional check.

USE app;

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
    status  varchar(20) NOT NULL,
    FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE = InnoDB;
INSERT INTO orders (id, user_id, total, status) VALUES
    (1, 1, 10.00, 'paid'),
    (2, 1, 20.00, 'open'),
    (3, 2, 150.00, 'open'),
    (4, 4, 5.00, 'open');

-- No primary key on purpose: execution compares whole rows.
CREATE TABLE audit_notes (
    user_id int NOT NULL,
    note    text
) ENGINE = InnoDB;
INSERT INTO audit_notes (user_id, note) VALUES
    (1, 'created'),
    (1, 'created'),
    (2, 'verified');

-- Non-transactional: dry runs and executions must refuse it.
CREATE TABLE legacy_notes (
    id   int PRIMARY KEY,
    note text
) ENGINE = MyISAM;
INSERT INTO legacy_notes (id, note) VALUES (1, 'kept forever');

CREATE USER 'dbairlock_app'@'%' IDENTIFIED BY 'app-secret';
GRANT SELECT, INSERT, UPDATE, DELETE ON app.* TO 'dbairlock_app'@'%';

CREATE USER 'dbairlock_super'@'%' IDENTIFIED BY 'super-secret';
GRANT ALL PRIVILEGES ON *.* TO 'dbairlock_super'@'%' WITH GRANT OPTION;
