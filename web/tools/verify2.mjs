import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1000, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));

const counts = await p.evaluate(() => ({
  rows: document.querySelectorAll('.dev-row').length,
  whys: document.querySelectorAll('.dev-why').length,
  currentTags: document.querySelectorAll('.dev-on').length,
}));
console.log('设备行/问号/当前在线:', JSON.stringify(counts));

// 点第一个问号
const why = await p.$('.dev-why');
if (why) { await why.click(); await new Promise(r => setTimeout(r, 800)); }
const modal = await p.evaluate(() => {
  const m = Array.from(document.querySelectorAll('.modal')).pop();
  if (!m) return null;
  return { title: m.querySelector('.modal-title')?.textContent?.trim(), lines: Array.from(m.querySelectorAll('.detail-grid')).map(e => e.innerText.replace(/\s+/g,' ').trim()) };
});
console.log('详情弹窗:', JSON.stringify(modal, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/verify-detail.png' });
await b.close();
