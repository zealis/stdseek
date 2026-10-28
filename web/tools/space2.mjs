import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1441, height: 900 });
await p.goto('http://127.0.0.1:8080/search?q=' + encodeURIComponent('上海'), { waitUntil: 'networkidle0', timeout: 40000 });
await new Promise(r => setTimeout(r, 1800));
console.log(JSON.stringify(await p.evaluate(() => {
  const kids = [...document.querySelector('app-root').children].map(c => {
    const r = c.getBoundingClientRect();
    return { tag: c.tagName, cls: (c.className||'').toString().slice(0,30), top: Math.round(r.top), bottom: Math.round(r.bottom), h: Math.round(r.height) };
  });
  return { kids, mainH: document.querySelector('main')?.scrollHeight };
})));
await b.close();
