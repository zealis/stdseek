import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 1100, deviceScaleFactor: 1 });
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 25000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2200));
await p.goto('http://localhost:8080/manage/features', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1000));

// 点 Turnstile，看字段是否展开
const chips = await p.$$('.card .chip');
for (const c of chips) {
  const t = (await c.evaluate((el) => el.textContent)) ?? '';
  if (t.includes('Cloudflare')) { await c.click(); break; }
}
await new Promise(r => setTimeout(r, 400));
const fields = await p.$$eval('.card .auth-field > span', els => els.map(e => e.textContent.replace(/\s+/g, ' ').trim()));
console.log('Turnstile 字段:', fields.join(' | '));
await p.screenshot({ path: 'D:/project/voidxo/shots/captcha-config2.png', fullPage: true });
await b.close();
