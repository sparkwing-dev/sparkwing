CREATE TABLE invoices (
    id         TEXT PRIMARY KEY,
    account_id TEXT NOT NULL REFERENCES accounts (id),
    cents      BIGINT NOT NULL CHECK (cents >= 0),
    paid       BOOLEAN NOT NULL DEFAULT false
);

CREATE INDEX invoices_account_idx ON invoices (account_id);
