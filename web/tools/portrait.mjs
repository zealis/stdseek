import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 1000, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
await p.evaluate(() => {
  for (const r of Array.from(document.querySelectorAll('tr'))) {
    if (r.innerText.includes('admin@example.com')) {
      const btn = Array.from(r.querySelectorAll('button')).find(b => b.textContent.trim() === '画像');
      if (btn) { btn.click(); return; }
    }
  }
});
await new Promise(r => setTimeout(r, 4000));
const info = await p.evaluate(() => {
  const m = Array.from(document.querySelectorAll('.modal')).pop();
  return m ? m.innerText.replace(/\s+/g, ' ').slice(0, 400) : null;
});
console.log('画像:', info);
await p.screenshot({ path: 'D:/project/voidxo/shots/portrait.png' });
await b.close();
