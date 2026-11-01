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
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const src = readFileSync(join(root, '..', 'CHANGELOG.md'), 'utf8');

const VERSION_RE = /^##\s+(v[0-9][0-9.]*)\s*[（(]\s*(.+?)\s*[）)]\s*$/;
const ITEM_RE = /^-\s+\*\*(.+?)\*\*\s*[：:]\s*(.+)$/;

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
console.log(`[changelog] ${out.length} 个版本，${(json.length / 1024).toFixed(0)}KB`);
