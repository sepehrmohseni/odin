// Demo traffic for the quickstart stack (`scripts/quickstart.sh --demo`): a steady, varying mix
// of requests through the gateway so the admin's dashboard, monitoring page and request log
// have something to show. It signs its own JWTs with the stack's ODIN_JWT_SECRET, and on
// purpose also sends requests the gateway rejects (no token, a bad token, unknown paths) and
// bursts that hit the categories service's rate limit.
//
//   GATEWAY=http://odin:8080 JWT_SECRET=... RATE=6 node traffic/generate.js
'use strict';

const crypto = require('crypto');

const gateway = (process.env.GATEWAY || 'http://odin:8080').replace(/\/$/, '');
const secret = process.env.JWT_SECRET || '';
const baseRate = Number(process.env.RATE || 6); // requests per second, on average

function b64url(buf) {
  return Buffer.from(buf).toString('base64').replace(/=+$/, '').replace(/\+/g, '-').replace(/\//g, '_');
}

function sign(claims) {
  const header = b64url(JSON.stringify({ alg: 'HS256', typ: 'JWT' }));
  const body = b64url(JSON.stringify(claims));
  const sig = b64url(crypto.createHmac('sha256', secret).update(header + '.' + body).digest());
  return header + '.' + body + '.' + sig;
}

const users = ['usr-001', 'usr-002', 'usr-003'];
function token(user) {
  const now = Math.floor(Date.now() / 1000);
  return sign({ sub: user, user_id: user, iat: now, exp: now + 3600 });
}

const pick = (a) => a[Math.floor(Math.random() * a.length)];
const bearer = (user) => ({ Authorization: 'Bearer ' + token(user || pick(users)) });

// [weight, request]. Paths and IDs exist in the demo backends unless marked otherwise.
const scenarios = [
  [24, () => ({ path: '/api/products' })],
  [12, () => ({ path: '/api/products/' + pick(['prod-001', 'prod-002', 'prod-003']) })],
  [3, () => ({ path: '/api/products/prod-404' })], // 404 from the backend
  [10, () => ({ path: '/api/users' })],
  [8, () => ({ path: '/api/users/' + pick(users), headers: bearer() })],
  [8, () => ({ path: '/api/categories' })],
  [4, () => ({ path: '/api/categories/' + pick(['cat-001', 'cat-002']) })],
  [10, () => ({ path: '/api/orders', headers: bearer() })],
  [4, () => ({ path: '/api/orders/' + pick(['ord-001', 'ord-002']), headers: bearer() })],
  [4, () => {
    const user = pick(users);
    return {
      method: 'POST',
      path: '/api/orders',
      headers: Object.assign({ 'Content-Type': 'application/json' }, bearer(user)),
      body: JSON.stringify({ userId: user, items: [{ productId: pick(['prod-001', 'prod-002']), quantity: 1 + Math.floor(Math.random() * 3), price: 19.99 }] }),
    };
  }],
  [4, () => ({ path: '/api/orders' })], // 401: no token
  [2, () => ({ path: '/api/orders', headers: { Authorization: 'Bearer not.a.token' } })], // 401
  [2, () => ({ path: pick(['/api/v2/products', '/api/payments', '/wp-login.php']) })], // 404: no route
  [6, () => ({ path: '/api/inventory' })], // the unreliable demo backend
  [4, () => ({ path: '/api/inventory/' + pick(['prod-001', 'prod-002', 'prod-003', 'prod-999']) })],
];
const totalWeight = scenarios.reduce((s, [w]) => s + w, 0);

function nextRequest() {
  let r = Math.random() * totalWeight;
  for (const [w, make] of scenarios) {
    if ((r -= w) < 0) return make();
  }
  return scenarios[0][1]();
}

let sent = 0;
let failed = 0;

async function send(req) {
  try {
    const res = await fetch(gateway + req.path, {
      method: req.method || 'GET',
      headers: req.headers,
      body: req.body,
      signal: AbortSignal.timeout(10000),
    });
    await res.arrayBuffer();
    sent++;
  } catch (e) {
    failed++;
  }
}

// The rate drifts over a few minutes, so the charts are not a flat line.
function currentRate() {
  const t = Date.now() / 1000;
  return Math.max(0.5, baseRate * (1 + 0.45 * Math.sin(t / 47) + 0.2 * Math.sin(t / 11)));
}

async function loop() {
  for (;;) {
    send(nextRequest());
    await new Promise((r) => setTimeout(r, (1000 / currentRate()) * (0.5 + Math.random())));
  }
}

// Every two minutes, a burst at the categories service, whose limit is 120 requests a minute
// per client: the tail of the burst is answered 429.
async function bursts() {
  for (;;) {
    await new Promise((r) => setTimeout(r, 120000));
    await Promise.all(Array.from({ length: 140 }, () => send({ path: '/api/categories' })));
  }
}

setInterval(() => {
  console.log(`traffic: ${sent} sent, ${failed} failed (rate ${currentRate().toFixed(1)}/s)`);
}, 60000);

if (!secret) {
  console.log('traffic: JWT_SECRET is not set; requests to the orders service will be rejected');
}
console.log(`traffic: sending about ${baseRate} requests/s to ${gateway}`);
loop();
bursts();
