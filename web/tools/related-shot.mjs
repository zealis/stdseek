import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 900, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8080/search?q=' + encodeURIComponent('测试'), { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 2500));
const info = await p.evaluate(() => ({
  hasAside: !!document.querySelector('.page-results.has-aside'),
  title: document.querySelector('.related-title')?.innerText.replace(/\s+/g,' ').trim(),
  items: Array.from(document.querySelectorAll('.related-item')).map(a => a.innerText.trim()),
  asideWidth: Math.round(document.querySelector('.results-aside')?.getBoundingClientRect().width || 0),
}));
console.log(JSON.stringify(info, null, 1));
const box = await p.evaluate(() => {
  const el = document.querySelector('.results-aside');
  const r = el?.getBoundingClientRect();
  return r ? { x: r.x - 8, y: r.y - 8, width: r.width + 16, height: Math.min(r.height + 16, 860) } : null;
});
if (box) await p.screenshot({ path: 'D:/project/stdseek/shots/related.png', clip: box });
await p.screenshot({ path: 'D:/project/stdseek/shots/related-full.png' });
await b.close();
