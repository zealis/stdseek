import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 1000, deviceScaleFactor: 1 });

await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 25000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2200));
console.log('登录后:', p.url());

// 设置页账户区按钮
await p.goto('http://localhost:8080/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 900));
const btns = await p.$$eval('.card button', els => els.map(e => e.textContent.trim()).filter(Boolean));
console.log('账户区按钮:', btns.join(' | '));

// 后台账号页：搜索 + 批量
await p.goto('http://localhost:8080/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const before = await p.$$eval('button', els => els.map(e => e.textContent.trim()));
console.log('选中前有批量按钮:', ['设为管理员','设为普通','禁用'].some(t => before.includes(t)));
await p.screenshot({ path: 'D:/project/voidxo/shots/users-nosel.png', fullPage: true });

const checks = await p.$$('tbody input[type="checkbox"]');
if (checks.length) { await checks[0].click(); await new Promise(r => setTimeout(r, 500)); }
const after = await p.$$eval('button', els => els.map(e => e.textContent.trim()));
console.log('选中后有批量按钮:', ['设为管理员','设为普通','禁用'].some(t => after.includes(t)));
console.log('已选提示:', (await p.evaluate(() => document.body.innerText)).match(/已选 \d+/)?.[0] ?? '(无)');
await p.screenshot({ path: 'D:/project/voidxo/shots/users-batch.png', fullPage: true });

await b.close();
