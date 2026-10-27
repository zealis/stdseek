import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1000, height: 1400, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8080/privacy', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 1200));
const info = await p.evaluate(() => ({
  title: document.querySelector('h1')?.textContent?.trim(),
  dates: document.querySelector('p.small')?.textContent?.replace(/\s+/g,' ').trim(),
  sections: Array.from(document.querySelectorAll('.section-title')).map(e => e.textContent.trim()),
  footer: Array.from(document.querySelectorAll('footer a')).map(a => a.textContent.trim()),
}));
console.log(JSON.stringify(info, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/privacy.png', fullPage: true });
await b.close();
