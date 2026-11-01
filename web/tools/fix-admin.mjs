import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1100, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));

// 站点信息页
await p.goto('http://127.0.0.1:8081/manage/site', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const site = await p.evaluate(() => {
  const labels = Array.from(document.querySelectorAll('.field'));
  const gaps = labels.slice(0, -1).map(l => {
    const a = l.getBoundingClientRect();
    const next = labels[labels.indexOf(l) + 1]?.getBoundingClientRect();
    return next ? Math.round(next.top - a.bottom) : null;
  });
  return { fields: labels.length, gaps, labelsHaveClass: labels.every(l => !!l.querySelector('.field-label')) };
});
console.log('站点信息:', JSON.stringify(site));
const box = await p.evaluate(() => { const r = document.querySelector('.card').getBoundingClientRect(); return { x: r.x, y: r.y, width: r.width, height: r.height }; });
await p.screenshot({ path: 'D:/project/stdseek/shots/fix-site.png', clip: box });

// 设置页搜索偏好
await p.goto('http://127.0.0.1:8081/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const pref = await p.evaluate(() => {
  const card = Array.from(document.querySelectorAll('.card')).find(c => c.innerText.includes('搜索偏好'));
  if (!card) return null;
  const r = card.getBoundingClientRect();
  return { text: card.innerText.replace(/\s+/g,' ').trim(), box: { x: r.x, y: r.y, width: r.width, height: r.height } };
});
console.log('搜索偏好:', JSON.stringify(pref));
if (pref) await p.screenshot({ path: 'D:/project/stdseek/shots/fix-pref.png', clip: pref.box });
await b.close();
