import puppeteer from 'puppeteer-core';
const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });
const p = await b.newPage();
await p.setViewport({ width: 1280, height: 900, deviceScaleFactor: 1 });
await p.goto('http://localhost:8080/login', { waitUntil: 'networkidle0', timeout: 30000 });
await new Promise(r => setTimeout(r, 900));

// 1) 表单里不应该有验证控件
const before = await p.evaluate(() => ({
  hasWidget: !!document.querySelector('vx-captcha-widget'),
  hasDialog: !!document.querySelector('.modal'),
  fields: Array.from(document.querySelectorAll('.auth-field > span')).map(e => e.textContent.trim()),
}));
console.log('打开时:', JSON.stringify(before));
await p.screenshot({ path: 'D:/project/stdseek/shots/login-clean.png' });

// 2) 填账号密码点登录 → 应弹窗
await p.type('input[type="email"]', 'admin@example.com');
await p.type('input[type="password"]', 'hunter2hunter2');
await p.click('button[type="submit"]');
await new Promise(r => setTimeout(r, 1200));
const dialog = await p.evaluate(() => {
  const m = document.querySelector('.modal');
  if (!m) return null;
  return {
    title: m.querySelector('.modal-title')?.textContent?.trim(),
    question: m.querySelector('.field > span')?.textContent?.trim(),
  };
});
console.log('弹窗:', JSON.stringify(dialog));
await p.screenshot({ path: 'D:/project/stdseek/shots/login-captcha-dialog.png' });

// 3) 答题继续
if (dialog?.question) {
  const m = dialog.question.match(/(\d+)\s*\+\s*(\d+)/);
  if (m) {
    await p.type('.modal input', String(Number(m[1]) + Number(m[2])));
    for (const btn of await p.$$('.modal button')) {
      const t = (await btn.evaluate(el => el.textContent)) ?? '';
      if (t.trim() === '继续') { await btn.click(); break; }
    }
    await new Promise(r => setTimeout(r, 2500));
    console.log('提交后地址:', p.url());
  }
}
await b.close();
