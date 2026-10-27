import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1100, height: 800, deviceScaleFactor: 2 });

await p.goto('http://127.0.0.1:8081/', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 1000));
const home = await p.evaluate(() => {
  const el = document.querySelector('.searchbar');
  if (!el) return null;
  const cs = getComputedStyle(el);
  return { shadow: cs.boxShadow, radius: cs.borderRadius };
});
console.log('首页搜索框:', JSON.stringify(home));
const box = await p.evaluate(() => {
  const r = document.querySelector('.searchbar').getBoundingClientRect();
  return { x: r.x - 12, y: r.y - 12, width: r.width + 24, height: r.height + 24 };
});
await p.screenshot({ path: 'D:/project/voidxo/shots/shadow-home.png', clip: box });

await p.goto('http://127.0.0.1:8081/search?q=go', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 1500));
const sp = await p.evaluate(() => {
  const el = document.querySelector('.searchbar');
  if (!el) return null;
  return { shadow: getComputedStyle(el).boxShadow };
});
console.log('搜索页搜索框:', JSON.stringify(sp));
const box2 = await p.evaluate(() => {
  const r = document.querySelector('.searchbar').getBoundingClientRect();
  return { x: r.x - 12, y: r.y - 12, width: r.width + 24, height: r.height + 24 };
});
await p.screenshot({ path: 'D:/project/voidxo/shots/shadow-search.png', clip: box2 });
await b.close();
