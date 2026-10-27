import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 900, height: 1000, deviceScaleFactor: 2 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));
await p.goto('http://127.0.0.1:8081/settings', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));

const info = await p.evaluate(() => {
  const card = Array.from(document.querySelectorAll('.card')).find(c => c.textContent?.includes('最近登录的设备'));
  if (!card) return null;
  return {
    rows: Array.from(card.querySelectorAll('.dev-row')).map(r => r.innerText.replace(/\s+/g, ' ').trim()),
  };
});
console.log(JSON.stringify(info, null, 1));
const card = await p.evaluateHandle(() => Array.from(document.querySelectorAll('.card')).find(c => c.textContent?.includes('最近登录的设备')));
if (card) {
  const box = await card.asElement().boundingBox();
  await p.screenshot({ path: 'D:/project/voidxo/shots/devices.png', clip: box });
}
await b.close();
