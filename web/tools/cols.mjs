import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 800, deviceScaleFactor: 1 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const cols = await p.$$eval('table thead th', els => els.map(e => e.textContent.trim()));
console.log('列:', JSON.stringify(cols));
// 打开画像看是否仍有注册
await p.evaluate(() => {
  for (const r of Array.from(document.querySelectorAll('tr'))) {
    if (r.innerText.includes('admin@example.com')) {
      const btn = Array.from(r.querySelectorAll('button')).find(b => b.textContent.trim() === '画像');
      if (btn) { btn.click(); return; }
    }
  }
});
await new Promise(r => setTimeout(r, 1500));
const head = await p.evaluate(() => {
  const m = Array.from(document.querySelectorAll('.modal')).pop();
  return m ? m.innerText.replace(/\s+/g, ' ').slice(0, 120) : null;
});
console.log('画像头部:', head);
await p.screenshot({ path: 'D:/project/stdseek/shots/users-cols.png' });
await b.close();
