import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 800));
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 1500));
const info = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return { modal: false };
  return {
    modal: true,
    fields: m.querySelectorAll('.field').length,
    inputs: m.querySelectorAll('input').length,
    text: m.innerText.replace(/\s+/g, ' ').trim().slice(0, 120),
    childCount: m.children.length,
    tail: m.innerHTML.slice(-300).replace(/\s+/g, ' '),
  };
});
console.log(JSON.stringify(info, null, 1));
await b.close();
