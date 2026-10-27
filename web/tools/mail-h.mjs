import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1100, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/features', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));

for (const btn of await p.$$('button')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.trim() === '配置') { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 800));

const m = await p.evaluate(() => {
  const modal = document.querySelector('.modal');
  if (!modal) return null;
  const input = Array.from(modal.querySelectorAll('input')).find(i => i.type === 'email');
  const btn = Array.from(modal.querySelectorAll('button')).find(b => b.textContent.includes('发送测试邮件'));
  const r1 = input?.getBoundingClientRect();
  const r2 = btn?.getBoundingClientRect();
  return {
    inputH: r1 ? Math.round(r1.height) : 0,
    inputTop: r1 ? Math.round(r1.top) : 0,
    btnH: r2 ? Math.round(r2.height) : 0,
    btnTop: r2 ? Math.round(r2.top) : 0,
    sameBottom: r1 && r2 ? Math.abs((r1.top + r1.height) - (r2.top + r2.height)) < 1 : false,
  };
});
console.log(JSON.stringify(m));
const box = await p.evaluate(() => {
  const modal = document.querySelector('.modal');
  const btn = Array.from(modal.querySelectorAll('button')).find(b => b.textContent.includes('发送测试邮件'));
  const row = btn?.parentElement?.getBoundingClientRect();
  return row ? { x: row.x - 10, y: row.y - 10, width: row.width + 20, height: row.height + 20 } : null;
});
if (box) await p.screenshot({ path: 'D:/project/voidxo/shots/mail-height.png', clip: box });
await b.close();
