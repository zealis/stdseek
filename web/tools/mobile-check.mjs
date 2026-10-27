import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 390, height: 844, deviceScaleFactor: 3, isMobile: true });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const geo = await p.evaluate(() => {
  const td = Array.from(document.querySelectorAll('td.email-cell')).find(t => t.innerText.includes('admin@example.com'));
  if (!td) return null;
  const email = td.querySelector('.cell-strong');
  const chip = td.querySelector('.chip');
  const a = email?.getBoundingClientRect(), c = chip?.getBoundingClientRect();
  return {
    sameRow: a && c ? Math.abs(a.top - c.top) < 6 : null,
    emailTop: a?.top, chipTop: c?.top,
  };
});
console.log('移动端邮箱行:', JSON.stringify(geo));
const box = await p.evaluate(() => {
  const td = document.querySelector('td.email-cell');
  const r = td?.getBoundingClientRect();
  return r ? { x: r.x - 6, y: r.y - 6, width: r.width + 12, height: r.height + 12 } : null;
});
if (box) await p.screenshot({ path: 'D:/project/voidxo/shots/mobile-email.png', clip: box });
await b.close();
