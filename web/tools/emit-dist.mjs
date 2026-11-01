// 把 Angular 构建产物（web/dist/stdseek-web/browser）同步到 internal/webui/dist，
// 供 go:embed 打包进单二进制。
//
// 不用 cp -r：Windows 下 npm 走 cmd，没有 cp。这里用 node 的 fs 保证跨平台。
import { rm, cp, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { generateChangelog } from './gen-changelog.mjs';

const src = resolve('dist/stdseek-web/browser');
const dst = resolve('../internal/webui/dist');

if (!existsSync(src)) {
  console.error(`[emit-dist] 缺少构建产物：${src}（先跑 npm run build）`);
  process.exit(1);
}

await rm(dst, { recursive: true, force: true });
await cp(src, dst, { recursive: true });

// 更新日志在这里再生成一次。
//
// 它平时由 `npm run build` 里的 gen-changelog 产出，但直接跑 `ng build`（排查构建问题时
// 常用的那条路）就绕过了，产物里的 changelog.json 会一直停在旧版本——关于弹窗显示
// 「最新版本是 v0.86」而实际已经 0.88，这种错没人会怀疑到构建脚本头上。
// 放在同步这一步兜住：只要产物进了 binary，日志一定是当前的。
const { count } = generateChangelog();
await cp(resolve('public/changelog.json'), join(dst, 'changelog.json'));

console.log(`[emit-dist] ${src} -> ${dst}（更新日志 ${count} 个版本）`);
