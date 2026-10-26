// 交互验证：标签反馈弹窗、提交进度页、后台标签反馈列表。
//
// 用法：npm run verify -- <baseUrl> <ticket> <shotsDir>
//   npm run verify -- http://127.0.0.1:18095 vxcu7e6f5rvi3xmo44 ../shots
//
// 为什么需要它：curl 只能验接口，验不了「Chip 上显示的是不是中文」「点了误判会不会真提交」
// 「后台表格有没有数据」这类只有渲染后才知道的事。
const puppeteer = require('puppeteer-core');

const [, , base = 'http://127.0.0.1:18095', ticket = '', outDir = '../shots'] = process.argv;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

(async () => {
  const browser = await puppeteer.launch({
    executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe',
    headless: 'new',
    args: ['--no-sandbox', '--disable-gpu'],
  });
  try {
    const page = await browser.newPage();
    await page.setViewport({ width: 1280, height: 940 });
    // 后台页面需要令牌，提前塞进 localStorage（服务端启动时固定为 dev）
    await page.evaluateOnNewDocument(() => localStorage.setItem('voidxo.adminToken', 'dev'));

    // ---- 1) 结果页：标签中文名 + 误判入口 ----
    await page.goto(`${base}/search?q=${encodeURIComponent('分布式')}`, { waitUntil: 'networkidle2' });
    await sleep(1200);
    const chips = await page.evaluate(() => {
      const btns = [...document.querySelectorAll('button')]
        .filter((b) => (b.textContent || '').includes('标签有误'));
      const chipTexts = [...document.querySelectorAll('.MuiChip-label')]
        .map((n) => n.textContent.trim())
        .filter((t) => ['疑似 AI 生成', '广告较多', '个人站点', 'UGC 平台'].includes(t));
      return { feedbackButtons: btns.length, chipTexts: [...new Set(chipTexts)] };
    });
    console.log('result page:', JSON.stringify(chips));
    await page.screenshot({ path: `${outDir}/result-labels.png` });

    await page.evaluate(() => {
      [...document.querySelectorAll('button')].filter((b) => (b.textContent || '').includes('标签有误'))[0].click();
    });
    await sleep(800);
    const dlg = await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"]');
      if (!d) return null;
      return {
        title: d.querySelector('h2')?.textContent || '',
        reasons: [...d.querySelectorAll('p')].map((p) => p.textContent.trim()).filter((t) => t.startsWith('判定理由')),
        checkboxes: d.querySelectorAll('input[type="checkbox"]').length,
      };
    });
    console.log('feedback dialog:', JSON.stringify(dlg));
    await page.screenshot({ path: `${outDir}/label-feedback-dialog.png` });

    // 勾第一个标签并提交，确认真发请求、真出反馈
    await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"]');
      d.querySelector('input[type="checkbox"]').click();
    });
    await sleep(200);
    await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"]');
      const btn = [...d.querySelectorAll('button')].find((b) => (b.textContent || '').includes('提交'));
      btn.click();
    });
    await sleep(1500);
    const submitted = await page.evaluate(() => {
      const d = document.querySelector('[role="dialog"]');
      const alert = d ? d.querySelector('[role="alert"], .MuiAlert-root') : null;
      return alert ? alert.textContent.trim() : '(无提示)';
    });
    console.log('feedback result:', submitted);
    await page.screenshot({ path: `${outDir}/label-feedback-submitted.png` });

    // ---- 2) 提交进度页 ----
    await page.goto(`${base}/contribute?ticket=${encodeURIComponent(ticket)}`, { waitUntil: 'networkidle2' });
    await sleep(1200);
    const progress = await page.evaluate(() => {
      const text = document.body.innerText;
      return {
        ticketShown: text.includes('查询码'),
        statusChip: [...document.querySelectorAll('.MuiChip-label')].map((n) => n.textContent.trim()),
        hasSteps: ['已提交', '人工审核', '抓取入库'].every((s) => text.includes(s)),
      };
    });
    console.log('contribute status page:', JSON.stringify(progress));
    await page.screenshot({ path: `${outDir}/contribute-status.png` });

    // ---- 3) 后台标签反馈列表 ----
    await page.goto(`${base}/manage/labels`, { waitUntil: 'networkidle2' });
    await sleep(1500);
    const admin = await page.evaluate(() => {
      const rows = document.querySelectorAll('tbody tr');
      return {
        rows: rows.length,
        firstRow: rows.length ? rows[0].innerText.replace(/\n/g, ' | ') : '',
        summary: [...document.querySelectorAll('.MuiChip-label')].map((n) => n.textContent.trim()).slice(0, 4),
      };
    });
    console.log('admin labels page:', JSON.stringify(admin));
    await page.screenshot({ path: `${outDir}/admin-labels.png` });
  } finally {
    await browser.close();
  }
})();
