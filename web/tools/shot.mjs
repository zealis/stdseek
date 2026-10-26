// 视觉冒烟：用 puppeteer-core 驱动本机 Chrome 无头截屏，验证 DESIGN.md 的移动端实现。
// 用法：node tools/shot.mjs [baseUrl]（默认 http://127.0.0.1:18080）
import puppeteer from 'puppeteer-core';
import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';

const BASE = process.argv[2] ?? 'http://127.0.0.1:18080';
const OUT = resolve('shots');
await mkdir(OUT, { recursive: true });

const browser = await puppeteer.launch({
  executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe',
  headless: 'new',
});

async function shot(name, path, { mobile = true, dark = false, before = null } = {}) {
  const page = await browser.newPage();
  if (mobile) {
    await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  } else {
    await page.setViewport({ width: 1280, height: 800, deviceScaleFactor: 1 });
  }
  if (dark) {
    await page.emulateMediaFeatures([{ name: 'prefers-color-scheme', value: 'dark' }]);
  }
  await page.goto(BASE + path, { waitUntil: 'networkidle0', timeout: 15000 });
  if (before) await before(page);
  await page.screenshot({ path: `${OUT}/${name}.png` });
  await page.close();
  console.log(`shot: ${name}`);
}

// 管理页需要先种令牌
async function withAdmin(fn) {
  return async (page) => {
    await page.evaluate(() => localStorage.setItem('voidxo.adminToken', 'testtoken'));
    await page.reload({ waitUntil: 'networkidle0' });
    await new Promise((r) => setTimeout(r, 600));
    if (fn) await fn(page);
  };
}

await shot('01-home-mobile', '/');
await shot('02-home-mobile-dark', '/', { dark: true });
await shot('03-search-mobile', '/search?q=docker');
await shot('04-search-mobile-dark', '/search?q=docker', { dark: true });
await shot('05-contribute-mobile', '/contribute');
await shot('06-settings-mobile', '/settings');
await shot('07-manage-gate-mobile', '/manage/whitelist');
await shot('08-manage-review-mobile', '/manage/review', {
  before: await withAdmin(null),
});
await shot('09-manage-whitelist-mobile', '/manage/whitelist', {
  before: await withAdmin(null),
});
await shot('10-home-desktop', '/', { mobile: false });
await shot('11-search-desktop', '/search?q=docker', { mobile: false });

// 弹层（快照查看）移动端形态：点开结果卡上的「查看快照」
{
  const page = await browser.newPage();
  await page.setViewport({ width: 390, height: 844, deviceScaleFactor: 2, isMobile: true, hasTouch: true });
  await page.goto(BASE + '/search?q=docker', { waitUntil: 'networkidle0' });
  await new Promise((r) => setTimeout(r, 600));
  const btn = await page.waitForSelector('::-p-text(查看快照)', { timeout: 5000 }).catch(() => null);
  if (btn) {
    await btn.click();
    await new Promise((r) => setTimeout(r, 1500));
    await page.screenshot({ path: `${OUT}/12-snapshot-sheet-mobile.png` });
    console.log('shot: 12-snapshot-sheet-mobile');
  } else {
    console.log('skip: 12 (查看快照 button not found)');
  }
  await page.close();
}

await browser.close();
console.log(`done -> ${OUT}`);
