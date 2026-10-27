import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1400, height: 900, deviceScaleFactor: 1 });
await p.goto('http://127.0.0.1:8081/login', { waitUntil: 'networkidle0', timeout: 30000 });
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 2500));

// IP 管理页
await p.goto('http://127.0.0.1:8081/manage/ip', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 2000));
const ip = await p.evaluate(() => ({
  hasToolbar: !!document.querySelector('vx-admin-toolbar, .toolbar'),
  tables: document.querySelectorAll('table.table').length,
  headers: Array.from(document.querySelectorAll('table.table thead th')).map(t => t.textContent.trim()).slice(0, 10),
  hasPager: !!document.querySelector('vx-pager'),
  rows: document.querySelectorAll('table.table tbody tr').length,
}));
console.log('IP 管理:', JSON.stringify(ip));
await p.screenshot({ path: 'D:/project/voidxo/shots/ip-list.png', fullPage: true });

// 标签反馈页
await p.goto('http://127.0.0.1:8081/manage/labels', { waitUntil: 'networkidle0' });
await new Promise(r => setTimeout(r, 1500));
const lb = await p.evaluate(() => {
  const heads = Array.from(document.querySelectorAll('table.table thead th')).map(t => t.textContent.trim());
  const firstEdit = Array.from(document.querySelectorAll('button')).find(b => b.textContent.trim() === '修改');
  return { headers: heads, hasEdit: !!firstEdit };
});
console.log('标签反馈:', JSON.stringify(lb));
if (lb.hasEdit) {
  await p.evaluate(() => {
    const btn = Array.from(document.querySelectorAll('button')).find(b => b.textContent.trim() === '修改');
    btn?.click();
  });
  await new Promise(r => setTimeout(r, 800));
  const modal = await p.evaluate(() => {
    const m = document.querySelector('.modal');
    return m ? m.innerText.replace(/\s+/g,' ').slice(0, 120) : null;
  });
  console.log('修改弹窗:', modal);
  await p.screenshot({ path: 'D:/project/voidxo/shots/label-edit.png' });
}
await b.close();
