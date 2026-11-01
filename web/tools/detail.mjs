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

// 1) 点设备行的问号
const why = await p.$('.dev-why');
if (why) { await why.click(); await new Promise(r => setTimeout(r, 800)); }
const modal = await p.evaluate(() => {
  const m = Array.from(document.querySelectorAll('.modal')).pop();
  if (!m) return null;
  return {
    title: m.querySelector('.modal-title')?.textContent?.trim(),
    text: m.innerText.replace(/\s+/g, ' ').trim().slice(0, 200),
  };
});
console.log('详情弹窗:', JSON.stringify(modal, null, 1));
await p.screenshot({ path: 'D:/project/stdseek/shots/device-detail.png' });

// 2) 关详情 → 点退出 → 应有确认
await p.evaluate(() => { const m = Array.from(document.querySelectorAll('.modal')).pop(); m?.querySelector('.modal-x')?.click(); });
await new Promise(r => setTimeout(r, 500));
for (const btn of await p.$$('button')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.trim() === '退出') { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 700));
const confirm = await p.evaluate(() => {
  const m = Array.from(document.querySelectorAll('.modal')).pop();
  return m ? { title: m.querySelector('.modal-title')?.textContent?.trim(), buttons: Array.from(m.querySelectorAll('.btn')).map(b => b.textContent.trim()) } : null;
});
console.log('退出确认:', JSON.stringify(confirm));
await p.screenshot({ path: 'D:/project/stdseek/shots/settings-logout.png' });
await b.close();
