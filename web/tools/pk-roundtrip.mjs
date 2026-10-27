// 注册 → 退出 → 用通行密钥登录，验证 flags 修复是否奏效。
import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 1000 });
const client = await p.createCDPSession();
await client.send('WebAuthn.enable');
await client.send('WebAuthn.addVirtualAuthenticator', {
  options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
});

// 1) 密码登录 + 添加密钥
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2200));
await p.goto('http://localhost:8080/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 900));
for (const btn of await p.$$('button')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.trim() === '添加') { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 2500));
const added = await p.evaluate(() => {
  const card = Array.from(document.querySelectorAll('.card')).find(c => c.textContent?.includes('通行密钥'));
  return (card?.textContent ?? '').includes('添加于');
});
console.log('添加成功:', added);

// 2) 退出
const exit = await p.$('.nav-link-btn');
if (exit) { await exit.click(); await new Promise(r => setTimeout(r, 1200)); }

// 3) 通行密钥登录
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0' });
await p.type('input[type="email"]', 'admin@example.com');
for (const btn of await p.$$('button')) {
  const t = (await btn.evaluate(el => el.textContent)) ?? '';
  if (t.includes('用通行密钥登录')) { await btn.click(); break; }
}
await new Promise(r => setTimeout(r, 2800));
const err = await p.evaluate(() => document.querySelector('.notice-error')?.textContent?.trim() ?? '');
console.log('登录结果:', p.url());
console.log('错误提示:', err || '(无)');
await b.close();
