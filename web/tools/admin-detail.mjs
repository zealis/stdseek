import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1200, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));

// 点 admin@example.com 那一行的「画像」
const clicked = await p.evaluate(() => {
  const rows = Array.from(document.querySelectorAll('tr'));
  for (const r of rows) {
    if (r.innerText.includes('admin@example.com')) {
      const btn = Array.from(r.querySelectorAll('button')).find(b => b.textContent.trim() === '画像');
      if (btn) { btn.click(); return true; }
    }
  }
  return false;
});
console.log('找到 admin 行:', clicked);
await new Promise(r => setTimeout(r, 1500));
const before = await p.evaluate(() => ({ rows: document.querySelectorAll('.dev-row').length, whys: document.querySelectorAll('.dev-why').length }));
console.log('设备行/问号:', JSON.stringify(before));

const why = await p.$('.dev-why');
if (why) {
  await why.click();
  await new Promise(r => setTimeout(r, 900));
}
const modals = await p.evaluate(() => Array.from(document.querySelectorAll('.modal')).map(m => ({
  title: m.querySelector('.modal-title')?.textContent?.trim(),
  lines: Array.from(m.querySelectorAll('.detail-grid')).map(e => e.innerText.replace(/\s+/g,' ').trim()),
})));
console.log('弹窗:', JSON.stringify(modals, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/admin-detail.png' });
await b.close();
