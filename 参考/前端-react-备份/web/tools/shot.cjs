// 交互验证脚本：打开页面 → 点第一个「查看快照」→ 等 iframe 渲染 → 截图并打印 Dialog 状态。
//
// 用法：npm run shot -- <url> <out.png>
//   例：npm run shot -- "http://127.0.0.1:18094/search?q=%E5%8D%95%E5%85%83%E6%B5%8B%E8%AF%95" ../shots/snapshot-dialog.png
//
// 依赖本机 Chrome（puppeteer-core 不下载浏览器）。路径不同时改 executablePath。
// 为什么不用 Chrome 的 --screenshot：那个只能截静态页面，点不了按钮、验不了交互。
const puppeteer = require('puppeteer-core');

(async () => {
  const url = process.argv[2];
  const out = process.argv[3];
  const browser = await puppeteer.launch({
    executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: 'new',
    args: ['--no-sandbox', '--disable-gpu'],
  });
  try {
    const page = await browser.newPage();
    await page.setViewport({ width: 1280, height: 900 });
    await page.goto(url, { waitUntil: 'networkidle2', timeout: 30000 });

    const clicked = await page.evaluate(() => {
      const btns = [...document.querySelectorAll('button')]
        .filter((b) => b.textContent && b.textContent.includes('查看快照'));
      if (!btns.length) return 0;
      btns[0].click();
      return btns.length;
    });
    console.log('snapshot buttons found:', clicked);
    await new Promise((r) => setTimeout(r, 3000));

    const state = await page.evaluate(() => {
      const dlg = document.querySelector('[role="dialog"]');
      const frame = dlg ? dlg.querySelector('iframe') : null;
      return {
        dialog: !!dlg,
        title: dlg ? (dlg.querySelector('h2')?.textContent || '') : '',
        iframe: !!frame,
        sandbox: frame ? frame.getAttribute('sandbox') : null,
        srcdocLen: frame && frame.srcdoc ? frame.srcdoc.length : 0,
      };
    });
    console.log(JSON.stringify(state));
    await page.screenshot({ path: out });
  } finally {
    await browser.close();
  }
})();
