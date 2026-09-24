-- The schema of the database this example adopts. It was created by
-- something other than gormgate: no migration produced it, and there is no
-- migration history to go with it.

CREATE TABLE crm_customers (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    name      TEXT NOT NULL,
    email     TEXT NOT NULL UNIQUE,
    signed_up DATETIME
);

CREATE TABLE crm_contracts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    customer_id INTEGER NOT NULL,
    value_cents INTEGER NOT NULL,
    CONSTRAINT fk_crm_contracts_customer FOREIGN KEY (customer_id) REFERENCES crm_customers (id)
);

CREATE INDEX idx_crm_contracts_customer_id ON crm_contracts (customer_id);
