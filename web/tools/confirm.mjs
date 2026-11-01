import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1000, height: 800, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));

// 点顶栏「退出」
for (const btn of await p.$$('.nav-link-btn')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.includes('退出')) { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 700));
const info = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return null;
  return {
    title: m.querySelector('.modal-title')?.textContent?.trim(),
    body: m.querySelector('p')?.textContent?.trim(),
    buttons: Array.from(m.querySelectorAll('.btn')).map(b => b.textContent.trim()),
  };
});
console.log('确认弹窗:', JSON.stringify(info));
const box = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return null;
  const r = m.getBoundingClientRect();
  return { x: r.x - 10, y: r.y - 10, width: r.width + 20, height: r.height + 20 };
});
if (box) await p.screenshot({ path: 'D:/project/stdseek/shots/confirm-logout.png', clip: box });

// 取消后应仍在原页
const cancel = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  const b = Array.from(m.querySelectorAll('.btn')).find(x => x.textContent.trim() === '取消');
  b.click(); return true;
});
await new Promise(r => setTimeout(r, 500));
console.log('取消后仍登录:', (await p.evaluate(() => !!document.querySelector('.nav-link-btn'))) && cancel);
await b.close();
