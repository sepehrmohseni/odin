// Demo backend for `scripts/quickstart.sh --demo`: a warehouse API that is deliberately
// unreliable, so the admin shows how the gateway handles failures. About 4% of requests fail
// with a 500, about 3% take one to two seconds (past the service's 1 s timeout in
// config/compose.yaml), and for 30 seconds of every three minutes its health check fails.
const express = require('express');

const app = express();
const PORT = Number(process.env.PORT || 8086);

const stock = {
  'prod-001': { sku: 'prod-001', warehouse: 'AMS-1', available: 42, reserved: 3 },
  'prod-002': { sku: 'prod-002', warehouse: 'AMS-1', available: 7, reserved: 1 },
  'prod-003': { sku: 'prod-003', warehouse: 'FRA-2', available: 0, reserved: 0 },
};

const maintenance = () => Date.now() % 180000 < 30000;

app.get('/health', (req, res) => {
  if (maintenance()) {
    return res.status(503).json({ status: 'maintenance' });
  }
  res.json({ status: 'ok' });
});

app.use('/api/inventory', (req, res, next) => {
  const r = Math.random();
  if (r < 0.04) {
    return res.status(500).json({ error: 'warehouse backend error' });
  }
  if (r < 0.07) {
    return setTimeout(next, 1000 + Math.random() * 1000);
  }
  next();
});

app.get('/api/inventory', (req, res) => {
  res.json({ data: Object.values(stock) });
});

app.get('/api/inventory/:sku', (req, res) => {
  const item = stock[req.params.sku];
  if (!item) {
    return res.status(404).json({ error: 'Unknown SKU' });
  }
  res.json(item);
});

app.listen(PORT, () => {
  console.log(`Inventory service listening on port ${PORT}`);
});
