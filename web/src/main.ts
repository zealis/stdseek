import { VERSION } from '@angular/core';
import { bootstrapApplication } from '@angular/platform-browser';
import { appConfig } from './app/app.config';
import { App } from './app/app';
import { migrateLegacyStorage } from './app/core/storage';
import { initLocale } from './app/core/i18n';

/** 源码仓库地址。控制台横幅里拼 commit 链接用，fork 就改这一行。 */
const REPO = 'https://github.com/zealis/stdseek';

/**
 * 控制台横幅：版本号 + 提交 + 前端框架版本 + 版权。
 *
 * 为什么值得把版本号暴露出来：自托管部署最常见的一句话是「你说的这个问题我这儿复现不了」，
 * 而要确认「你跑的是哪个构建」在浏览器里只有开发者工具这一条路。
 * 代价是别人也知道你的版本——但这个站没有需要靠藏版本保护的东西，
 * 出问题时的可排查性更重要（Discourse 等自托管产品都是这么做的）。
 */
function logBanner(): void {
  const seed = typeof window === 'undefined' ? undefined : window.__STDSEEK_BRANDING__;
  const name = (seed?.name || 'StdSeek').trim();
  const appVersion = (seed?.app_version || '').trim();
  const commit = (seed?.commit || '').trim();
  const commitUrl = commit && commit !== 'none' ? `${REPO}/commit/${commit}` : '';

  const line = [appVersion ? `v${appVersion}` : '', commitUrl, `Angular v${VERSION.full}`]
    .filter(Boolean)
    .join(' — ');

  console.log(
    `%c ${name} %c ${line}`,
    'background:#0f766e;color:#fff;border-radius:3px;padding:1px 6px;font-weight:600',
    'color:#71717a',
  );
  // 版权单独一行：这一行会被人截图贴到 issue 里，所以要能独立读懂。
  console.log(
    `%c© ${new Date().getFullYear()} ${name} · MIT License · 原文版权归各站点所有`,
    'color:#a1a1aa',
  );
}

// 必须在 bootstrap 之前：服务一构造就会读 localStorage 里的令牌与偏好。
migrateLegacyStorage();

async function main(): Promise<void> {
  // 语言也要先定下来再 bootstrap：英文用户多等一次字典请求，换首帧就是英文
  // （否则会先闪中文骨架再整体换掉）。中文用户没有额外请求。
  await initLocale();
  logBanner();
  await bootstrapApplication(App, appConfig);
}

main().catch((err) => console.error(err));
