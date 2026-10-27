import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 900, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));
const box = await p.evaluate(() => {
  const row = document.querySelector('.dev-row');
  const r = row?.getBoundingClientRect();
  return r ? { x: r.x + r.width - 320, y: r.y - 6, width: 330, height: r.height + 12 } : null;
});
if (box) await p.screenshot({ path: 'D:/project/voidxo/shots/dev-spacing.png', clip: box });
const geom = await p.evaluate(() => {
  const on = document.querySelector('.dev-on');
  const why = document.querySelector('.dev-why');
  if (!on || !why) return null;
  const a = on.getBoundingClientRect(), c = why.getBoundingClientRect();
  return { gap: Math.round(c.left - a.right), sameRow: Math.abs(a.top - c.top) < 6 };
});
console.log('间距:', JSON.stringify(geom));
await b.close();
