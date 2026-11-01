import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 1000, deviceScaleFactor: 1 });
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 25000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://localhost:8080/manage/features', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));

const rows = await p.$$eval('.flag .section-title', els => els.map(e => e.textContent.trim()));
console.log('行标题:', rows.join(' | '));
await p.screenshot({ path: 'D:/project/stdseek/shots/features-list.png', fullPage: true });

// 打开邮件配置弹窗
for (const btn of await p.$$('button')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.trim() === '配置') { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 700));
const modal = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return null;
  return { title: m.querySelector('.modal-title')?.textContent?.trim(), fields: Array.from(m.querySelectorAll('.field > span')).map(e => e.textContent.trim()) };
});
console.log('弹窗:', JSON.stringify(modal));
await p.screenshot({ path: 'D:/project/stdseek/shots/features-modal.png' });
await b.close();
