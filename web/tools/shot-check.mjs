// 临时：验证搜索补全的下拉、键盘与选中跳转。
import puppeteer from 'puppeteer-core';

const browser = await puppeteer.launch({
  executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe',
  headless: 'new',
});
const page = await browser.newPage();
await page.setViewport({ width: 1280, height: 860, deviceScaleFactor: 1 });

await page.goto('http://127.0.0.1:8080/', { waitUntil: 'networkidle0', timeout: 25000 });
await page.type('.hero-form .searchbar-input', '分布式', { delay: 60 });
await new Promise((r) => setTimeout(r, 900));

const items = await page.$$eval('.suggest-item', (els) => els.map((e) => e.textContent.trim()));
console.log('补全条数:', items.length);
console.log('前三条:', items.slice(0, 3).join(' / '));
await page.screenshot({ path: 'D:/project/voidxo/shots/suggest-home.png' });

// 键盘下移一次再回车，应该带着补全词去搜索
await page.keyboard.press('ArrowDown');
await new Promise((r) => setTimeout(r, 150));
const activeText = await page.$eval('.suggest-item.is-active', (el) => el.textContent.trim());
console.log('键盘高亮项:', activeText);
await page.keyboard.press('Enter');
await new Promise((r) => setTimeout(r, 1800));
console.log('跳转后地址:', decodeURIComponent(page.url()));
const cards = await page.$$eval('.result-domain', (els) => els.length);
console.log('结果条数:', cards);

// 搜索页的 header 搜索框也要能补全
await page.click('.header-search .searchbar-input');
await page.keyboard.down('Control');
await page.keyboard.press('KeyA');
await page.keyboard.up('Control');
await page.type('.header-search .searchbar-input', 'redis', { delay: 60 });
await new Promise((r) => setTimeout(r, 900));
const hdr = await page.$$eval('.suggest-item', (els) => els.map((e) => e.textContent.trim()));
console.log('header 补全条数:', hdr.length, '| 首条:', hdr[0] ?? '(无)');
await page.screenshot({ path: 'D:/project/voidxo/shots/suggest-header.png' });

await browser.close();
