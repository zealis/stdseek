import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1400, height: 900, deviceScaleFactor: 2 });

// 结果页：量宽度
await p.goto('http://127.0.0.1:8080/search?q=' + encodeURIComponent('城市'), { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 2500));
const res = await p.evaluate(() => {
  const page = document.querySelector('.page-results');
  const main = document.querySelector('.results-main');
  const aside = document.querySelector('.results-aside');
  const card = document.querySelector('.results-main vx-result-card') || document.querySelector('.results-main .card');
  const r = (el) => el ? Math.round(el.getBoundingClientRect().width) : 0;
  return { pageW: r(page), mainW: r(main), asideW: r(aside), cardW: r(card) };
});
console.log('结果页宽度:', JSON.stringify(res));
await p.screenshot({ path: 'D:/project/voidxo/shots/fix-search.png' });
await b.close();
