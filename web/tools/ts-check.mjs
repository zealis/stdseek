import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 900 });
p.on('pageerror', e => console.log('[pageerror]', String(e).slice(0,120)));
p.on('requestfailed', r => { if (r.url().includes('cloudflare') || r.url().includes('challenges')) console.log('[failed]', r.url().slice(0,80)); });
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 800));
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 3000));
const info = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return { modal: false };
  const w = m.querySelector('.cap-widget');
  return {
    modal: true,
    hasIframe: !!m.querySelector('iframe'),
    widgetHtml: (w?.innerHTML ?? '').replace(/\s+/g, ' ').slice(0, 200),
    height: w ? Math.round(w.getBoundingClientRect().height) : 0,
  };
});
console.log(JSON.stringify(info, null, 1));
await p.screenshot({ path: 'D:/project/stdseek/shots/login-turnstile.png' });
await b.close();
