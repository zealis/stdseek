// 把根目录的 CHANGELOG.md 转成前端能直接 fetch 的 JSON（public/changelog.json）。
//
// 为什么要生成：更新日志只有一份事实来源（CHANGELOG.md），前端不该再抄一遍。
// 放 public/ 而不是打进 JS：170 个版本的文本有 100KB+，而「关于」弹窗一年打开几次，
// 塞进 main 等于让每个访客都下载它。
//
// 解析保持宽松：只认 `## vX.Y.Z（日期）` 与 `- **类型**：正文` 两种行，其余跳过。
// 历史上早期的条目格式不完全统一，跳过它们比报错好——弹窗里少一行不影响什么。
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');

const VERSION_RE = /^##\s+(v[0-9][0-9.]*)\s*[（(]\s*(.+?)\s*[）)]\s*$/;
const ITEM_RE = /^-\s+\*\*(.+?)\*\*\s*[：:]\s*(.+)$/;

/**
 * 读 CHANGELOG.md，写出 public/changelog.json，并把 JSON 字符串一起返回。
 *
 * 返回值给 emit-dist 用：它要往 internal/webui/dist 里再放一份，
 * 让「没走 npm run build、直接 ng build」的构建也能带上最新日志。
 *
 * @returns {{ count: number, bytes: number, json: string }}
 */
export function generateChangelog() {
  // CHANGELOG.md 在仓库根目录，而构建可能只拿到 web/（比如 Dockerfile 的前端阶段）。
  // 读不到时不抛：抛出去的表现是「npm run build 直接失败，报一个和前端毫无关系的
  // ENOENT」，而这一步只是生成一个展示用的日志。优先沿用上一次生成的文件。
  let src = '';
  try {
    src = readFileSync(join(root, '..', 'CHANGELOG.md'), 'utf8');
  } catch {
    const cached = join(root, 'public', 'changelog.json');
    try {
      const json = readFileSync(cached, 'utf8');
      console.warn('[changelog] 找不到 CHANGELOG.md，沿用 public/changelog.json');
      return { count: JSON.parse(json).length, bytes: json.length, json };
    } catch {
      console.warn('[changelog] 找不到 CHANGELOG.md，也没有可用的 changelog.json，写出空表');
      mkdirSync(join(root, 'public'), { recursive: true });
      writeFileSync(cached, '[]');
      return { count: 0, bytes: 2, json: '[]' };
    }
  }
  const out = [];
  let cur = null;

  for (const line of src.split('\n')) {
    const v = VERSION_RE.exec(line);
    if (v) {
      cur = { version: v[1], date: v[2], items: [] };
      out.push(cur);
      continue;
    }
    const it = ITEM_RE.exec(line);
    if (it && cur) {
      cur.items.push({ type: it[1].trim(), text: it[2].trim() });
    }
  }

  // 没有条目的版本（只有一行标题）也留着：日期本身也是信息。
  const json = JSON.stringify(out);
  const dir = join(root, 'public');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'changelog.json'), json);
  return { count: out.length, bytes: json.length, json };
}

// 直接 `node tools/gen-changelog.mjs` 时跑一次并打印；被 import 时只导出函数。
if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const { count, bytes } = generateChangelog();
  console.log(`[changelog] ${count} 个版本，${(bytes / 1024).toFixed(0)}KB`);
}
