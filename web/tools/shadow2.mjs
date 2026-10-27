import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });

// --- 桌面：搜索框阴影三态 ---
const p = await b.newPage();
await p.setViewport({ width: 1200, height: 800, deviceScaleFactor: 1 });
await p.goto('http://127.0.0.1:8080/', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 900));
// 先点空白处让输入框失焦
await p.evaluate(() => document.body.click());
await new Promise(r => setTimeout(r, 400));
const idle = await p.evaluate(() => getComputedStyle(document.querySelector('.searchbar')).boxShadow);
// 输入文字
await p.type('.searchbar-input', 'golang');
await p.evaluate(() => document.querySelector('.searchbar-input').blur());
await new Promise(r => setTimeout(r, 400));
const filled = await p.evaluate(() => getComputedStyle(document.querySelector('.searchbar')).boxShadow);
console.log('空且未聚焦:', idle === 'none' ? '无阴影 ✓' : idle);
console.log('有内容:', filled !== 'none' ? '有阴影 ✓' : '无阴影 ✗');

// --- 移动端：相关搜索 vs 分页顺序 ---
const m = await b.newPage();
await m.setViewport({ width: 390, height: 844, deviceScaleFactor: 2, isMobile: true });
await m.goto('http://127.0.0.1:8080/search?q=' + encodeURIComponent('城市'), { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 3000));
const order = await m.evaluate(() => {
  const a = document.querySelector('.results-aside')?.getBoundingClientRect();
  const g = document.querySelector('.results-pager')?.getBoundingClientRect();
  const main = document.querySelector('.results-main')?.getBoundingClientRect();
  return {
    asideTop: a ? Math.round(a.top + window.scrollY) : null,
    pagerTop: g ? Math.round(g.top + window.scrollY) : null,
    mainBottom: main ? Math.round(main.bottom + window.scrollY) : null,
    asideBeforePager: a && g ? a.top < g.top : null,
  };
});
console.log('移动端顺序:', JSON.stringify(order));
await m.screenshot({ path: 'D:/project/voidxo/shots/mobile-related.png', fullPage: true });
await b.close();
