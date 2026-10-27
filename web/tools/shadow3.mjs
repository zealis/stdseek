import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1200, height: 800, deviceScaleFactor: 1 });
await p.goto('http://127.0.0.1:8080/', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 900));

const read = () => p.evaluate(() => getComputedStyle(document.querySelector('.searchbar')).boxShadow);
const idle = await read();
console.log('1) 空 + 聚焦（首页默认）:', idle.includes('16, 24, 40') ? '有抬升阴影 ✗' : '无抬升阴影 ✓');

await p.evaluate(() => document.querySelector('.searchbar-input').blur());
await p.mouse.move(5, 700);
await new Promise(r => setTimeout(r, 400));
const blurred = await read();
console.log('2) 空 + 未悬停:', blurred === 'none' ? '无阴影 ✓' : blurred);

await p.hover('.searchbar');
await new Promise(r => setTimeout(r, 400));
const hovered = await read();
console.log('3) 悬停:', hovered.includes('16, 24, 40') ? '有阴影 ✓' : '无阴影 ✗');

await p.type('.searchbar-input', 'golang');
await p.evaluate(() => document.querySelector('.searchbar-input').blur());
await p.mouse.move(5, 700);
await new Promise(r => setTimeout(r, 400));
const filled = await read();
console.log('4) 有内容 + 未悬停:', filled.includes('16, 24, 40') ? '有阴影 ✓' : '无阴影 ✗');
await b.close();
