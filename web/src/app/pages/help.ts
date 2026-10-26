import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';

/**
 * 帮助页：搜索语法与检索范围。
 *
 * 这是用户可以看懂的说明书，允许出现解释性文字（AGENTS.md 设计铁律第 1 条的例外，
 * 与 /api-docs 同类）。首页和搜索框里不放这些说明——那里每多一句都是在偷注意力。
 */
@Component({
  selector: 'vx-help-page',
  imports: [RouterLink],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">帮助</h1>
      <p class="page-desc">搜索语法与检索范围。</p>

      <div class="card" style="margin-top: 16px">
        <div class="section-title">搜索技巧</div>
        <div class="syntax-list">
          @for (s of SYNTAX; track s.code) {
            <div class="syntax-row">
              <code class="mono syntax-code">{{ s.code }}</code>
              <span class="small">{{ s.desc }}</span>
            </div>
          }
        </div>

        <p class="faint small" style="margin-top: 14px">可以组合使用：</p>
        <pre class="code">+北京 -大学
site:php.net "getting started"
+redis -jedis site:redis.io</pre>
        <p class="faint small" style="margin-top: 8px">
          识别到的语法会显示在结果页的过滤行里，能确认它真的生效了。
        </p>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">检索范围</div>
        <ul class="scope">
          @for (s of SCOPE; track s) {
            <li>{{ s }}</li>
          }
        </ul>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">更多</div>
        <div class="row wrap" style="gap: 8px; margin-top: 10px">
          <a class="btn btn-outline btn-sm" routerLink="/api-docs">API 文档</a>
          <a class="btn btn-outline btn-sm" routerLink="/usage">每日用量</a>
        </div>
      </div>
    </div>
  `,
  styles: `
    .syntax-list {
      margin-top: 10px;
    }

    .syntax-row {
      display: grid;
      grid-template-columns: 118px 1fr;
      gap: 10px;
      align-items: baseline;
      padding: 7px 0;
      border-bottom: 1px solid var(--line);
    }

    .syntax-row:last-child {
      border-bottom: none;
    }

    .syntax-code {
      font-size: var(--fs-sm);
      color: var(--brand);
    }

    .code {
      margin: 8px 0 0;
      padding: 12px;
      background: var(--sunken);
      border-radius: var(--r-md);
      font-family: var(--font-mono);
      font-size: var(--fs-xs);
      line-height: 1.8;
      color: var(--text-2);
      white-space: pre-wrap;
      word-break: break-all;
    }

    .scope {
      margin: 10px 0 0;
      padding-left: 18px;
      color: var(--text-2);
      font-size: var(--fs-sm);
      line-height: 1.9;
    }

    @media (max-width: 639.98px) {
      .syntax-row {
        grid-template-columns: 1fr;
        gap: 2px;
      }
    }
  `,
})
export class HelpPage {
  protected readonly SYNTAX = [
    {
      code: '+词',
      desc: '结果必须包含这个词。写成 +北京 -大学，就是「包含北京、且不含大学」。',
    },
    {
      code: '-词',
      desc: '结果不能包含这个词。只写排除词也能用，但会返回大量无关内容。',
    },
    {
      code: '"短语"',
      desc: '作为一个整体匹配，不再拆成多个词。长查询或专有名词建议加引号，中英文引号都认。',
    },
    {
      code: 'site:域名',
      desc: '只在这个站点内搜索，自动包含它的子域。也可以直接粘贴整条网址。',
    },
  ];

  protected readonly SCOPE = [
    '只索引人工审核过的白名单站点，不做全网抓取——那些 SEO 农场和 AI 水文从一开始就进不来。',
    '中文查询只召回中文内容；中英文混排的查询才允许跨语言，靠术语词典桥接（命名空间 → namespaces）。',
    '结果页的「过滤」和「时间」是硬条件，不是降权：被排除的标签不会以低分形式漏进结果。',
    '每条结果都能回指原文：排序理由可展开，摘要的每条结论都挂着原文引用编号。',
  ];
}
