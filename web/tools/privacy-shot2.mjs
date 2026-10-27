import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1000, height: 1500, deviceScaleFactor: 1.5 });
await p.goto('http://127.0.0.1:8080/privacy', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 1500));
const info = await p.evaluate(() => ({
  dates: document.querySelector('p.small')?.textContent?.replace(/\s+/g,' ').trim(),
  sections: Array.from(document.querySelectorAll('.section-title')).map(e => e.textContent.trim()),
  contact: document.querySelectorAll('.card')[9]?.innerText.replace(/\s+/g,' ').trim(),
  minor: Array.from(document.querySelectorAll('.card')).find(c => c.innerText.includes('未成年人'))?.innerText.replace(/\s+/g,' ').trim(),
}));
console.log(JSON.stringify(info, null, 1));
await p.screenshot({ path: 'D:/project/voidxo/shots/privacy-v2.png', fullPage: true });
await b.close();
