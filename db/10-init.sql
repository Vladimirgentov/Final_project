

CREATE TABLE IF NOT EXISTS prices (
  id          BIGSERIAL PRIMARY KEY,           
  product_id  TEXT,                               
  created_at  DATE NOT NULL,
  name        TEXT NOT NULL,
  category    TEXT NOT NULL,
  price       NUMERIC(12,2) NOT NULL CHECK (price > 0),

  CONSTRAINT prices_uniq UNIQUE (created_at, name, category, price)
);


CREATE INDEX IF NOT EXISTS idx_prices_created_at ON prices (created_at);
CREATE INDEX IF NOT EXISTS idx_prices_price ON prices (price);
CREATE INDEX IF NOT EXISTS idx_prices_category ON prices (category);
