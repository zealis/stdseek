// 把 Angular 构建产物（web/dist/voidxo-web/browser）同步到 internal/webui/dist，
// 供 go:embed 打包进单二进制。
//
// 不用 cp -r：Windows 下 npm 走 cmd，没有 cp。这里用 node 的 fs 保证跨平台。
import { rm, cp } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { resolve } from 'node:path';

const src = resolve('dist/voidxo-web/browser');
const dst = resolve('../internal/webui/dist');

if (!existsSync(src)) {
  console.error(`[emit-dist] 缺少构建产物：${src}（先跑 npm run build）`);
  process.exit(1);
}

await rm(dst, { recursive: true, force: true });
await cp(src, dst, { recursive: true });
console.log(`[emit-dist] ${src} -> ${dst}`);
