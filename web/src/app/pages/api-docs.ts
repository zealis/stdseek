import { Component, signal } from '@angular/core';
import { Icon } from '../shared/ui/icon';

/**
 * API 文档页。
 *
 * 只做一件事：把 /api/search 的参数与字段讲清楚，并给一个能直接点的调试入口。
 * 这是唯一允许出现大段解释文字的地方——它本身就是文档，不是产品界面
 * （AGENTS.md 设计铁律第 1 条的例外）。
 */
@Component({
  selector: 'vx-api-docs-page',
  imports: [Icon],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">API</h1>
      <p class="page-desc">检索结果可以直接用 HTTP 调用，返回字段按需裁剪。</p>

      <div class="card" style="margin-top: 16px">
        <div class="row" style="gap: 8px; align-items: center">
          <span class="chip chip-accent mono">GET</span>
          <code class="mono" style="font-size: var(--fs-sm)">/api/search</code>
        </div>
        <p class="faint small" style="margin-top: 8px">
          返回检索结果与深网发现区。限流 {{ rateLimit }} 次/分钟/IP，超出返回 429。
          浏览器跨域调用需要服务端以 <code class="mono">-cors</code> 放开来源，默认只允许同源。
        </p>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">参数</div>
        <div style="margin-top: 8px">
          @for (p of PARAMS; track p.name) {
            <div class="api-param">
              <code class="mono">{{ p.name }}</code>
              <span class="small">{{ p.desc }}</span>
            </div>
          }
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">fields 可选字段</div>
        <p class="faint small" style="margin-top: 6px">
          不传返回全部；传了只返回点名的字段，其余不出现。
        </p>
        <div class="row wrap" style="margin-top: 10px; gap: 6px">
          @for (f of FIELDS; track f) {
            <span class="chip mono">{{ f }}</span>
          }
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">示例</div>
        <pre class="api-code">{{ curlSample }}</pre>
        <pre class="api-code">{{ jsSample }}</pre>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">试一试</div>
        <div class="row wrap" style="margin-top: 10px; gap: 8px">
          <input
            class="input"
            style="flex: 1; min-width: 200px"
            placeholder="查询词，如 kubernetes"
            [value]="q()"
            (input)="q.set($any($event.target).value)"
            (keydown.enter)="run()"
          />
          <button type="button" class="btn btn-solid" [disabled]="loading()" (click)="run()">
            @if (loading()) {
              <span class="spinner"></span>
            } @else {
              <vx-icon name="search" [size]="15" />
            }
            调用
          </button>
        </div>
        @if (error()) {
          <div class="notice notice-error" style="margin-top: 10px">{{ error() }}</div>
        }
        @if (result()) {
          <pre class="api-code">{{ result() }}</pre>
        }
      </div>
    </div>
  `,
  styles: `
    .api-param {
      display: grid;
      grid-template-columns: 120px 1fr;
      gap: 10px;
      align-items: baseline;
      padding: 7px 0;
      border-bottom: 1px solid var(--line);
    }

    .api-param:last-child {
      border-bottom: none;
    }

    .api-code {
      margin: 10px 0 0;
      padding: 12px;
      background: var(--sunken);
      border-radius: var(--r-md);
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
      line-height: 1.7;
      color: var(--text-2);
      white-space: pre-wrap;
      word-break: break-all;
      overflow: auto;
    }

    @media (max-width: 639.98px) {
      .api-param {
        grid-template-columns: 1fr;
        gap: 2px;
      }
    }
  `,
})
export class ApiDocsPage {
  protected readonly rateLimit = 120;

  protected readonly PARAMS = [
    { name: 'q', desc: '查询词，必填，最长 512 字符' },
    { name: 'size', desc: '每页条数，默认 10，上限 50' },
    { name: 'page', desc: '页码，从 1 开始' },
    { name: 'exclude', desc: '排除标签：ai / ads / personal / platform，逗号分隔（旧名 no 仍可用）' },
    { name: 'since', desc: '时间窗：7d / 12h / 4w / 1y，或直接给秒数；不传为全部' },
    { name: 'fields', desc: '只返回这些字段，逗号分隔，见下方清单' },
    { name: 'pretty', desc: '非 0 时缩进输出，方便 curl 直接看' },
  ];

  protected readonly FIELDS = [
    'url', 'domain', 'title', 'description', 'excerpt', 'site_name',
    'labels', 'label_reasons', 'score', 'reasons', 'breakdown',
    'time_by_fetch', 'sort_time', 'prosperity', 'snippets',
    'has_snapshot', 'fetchable', 'dup_count',
  ];

  protected readonly curlSample =
    'curl "http://127.0.0.1:8080/api/search?q=kubernetes&size=5&fields=url,title,score&pretty=1"';

  protected readonly jsSample =
    "const r = await fetch('/api/search?q=kubernetes&fields=url,title,score');\n" +
    'const { hits, total } = await r.json();';

  protected readonly q = signal('kubernetes');
  protected readonly loading = signal(false);
  protected readonly error = signal('');
  protected readonly result = signal('');

  // 用原始 fetch 而不是 PublicApi.search：这一页要展示服务端真正吐出的 JSON，
  // 经过类型化封装就看不到 fields 裁剪后的样子了。
  protected async run(): Promise<void> {
    const q = this.q().trim();
    if (!q || this.loading()) return;
    this.loading.set(true);
    this.error.set('');
    this.result.set('');
    try {
      const url = `/api/search?q=${encodeURIComponent(q)}&size=3&fields=url,title,score,has_snapshot&pretty=1`;
      const resp = await fetch(url);
      const text = await resp.text();
      if (!resp.ok) {
        let msg = text;
        try {
          msg = JSON.parse(text).error ?? text;
        } catch {
          /* 非 JSON 错误体，原样显示 */
        }
        this.error.set(`${resp.status} ${msg}`);
      } else {
        this.result.set(text);
      }
    } catch (e) {
      this.error.set((e as Error).message);
    } finally {
      this.loading.set(false);
    }
  }
}
