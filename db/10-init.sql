CREATE TABLE IF NOT EXISTS prices (
  product_id   TEXT NOT NULL,
  created_at   DATE NOT NULL,
  name         TEXT NOT NULL,
  category     TEXT NOT NULL,
  price        INTEGER NOT NULL CHECK (price > 0),
  CONSTRAINT prices_uniq UNIQUE (product_id, created_at, name, category, price)
);
