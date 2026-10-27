import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 800, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/manage/users', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1200));
const geo = await p.evaluate(() => {
  const rows = Array.from(document.querySelectorAll('tbody tr'));
  return rows.map(r => {
    const tds = Array.from(r.querySelectorAll('td'));
    const act = tds[tds.length - 1];
    const btns = Array.from(act.querySelectorAll('button'));
    const tops = btns.map(b => Math.round(b.getBoundingClientRect().top));
    return {
      email: tds[1]?.innerText.trim().split('\n')[0],
      buttons: btns.map(b => b.textContent.trim()),
      sameRow: new Set(tops).size <= 1,
      colWidth: Math.round(act.getBoundingClientRect().width),
    };
  });
});
console.log(JSON.stringify(geo, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/users-actions.png' });
await b.close();
