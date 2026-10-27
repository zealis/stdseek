import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 900, deviceScaleFactor: 1 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/ip', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 1500));
console.log('地址:', p.url());
const info = await p.evaluate(() => ({
  title: document.querySelector('.toolbar-title')?.textContent?.trim(),
  riskRows: Array.from(document.querySelectorAll('.card')).filter(c => c.textContent?.includes('高风险')).flatMap(c => Array.from(c.querySelectorAll('.flag')).length),
  text: document.body.innerText.replace(/\s+/g, ' ').slice(0, 250),
}));
console.log('页面:', JSON.stringify(info, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/ip-page.png', fullPage: true });
await b.close();
