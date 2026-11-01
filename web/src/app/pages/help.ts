import { Component } from '@angular/core';
import { RouterLink } from '@angular/router';
import { t } from '../core/i18n';

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
      <h1 class="page-title">{{ t('help.title') }}</h1>
      <p class="page-desc">{{ t('help.desc') }}</p>

      <div class="card" style="margin-top: 16px">
        <div class="section-title">{{ t('help.tips') }}</div>
        <div class="syntax-list">
          @for (s of SYNTAX; track s.codeKey) {
            <div class="syntax-row">
              <code class="mono syntax-code">{{ t(s.codeKey) }}</code>
              <span class="small">{{ t(s.descKey) }}</span>
            </div>
          }
        </div>

        <p class="faint small" style="margin-top: 14px">{{ t('help.combine') }}</p>
        <pre class="code">{{ t('help.example') }}</pre>
        <p class="faint small" style="margin-top: 8px">
          {{ t('help.syntaxHint') }}
        </p>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">{{ t('help.scope') }}</div>
        <ul class="scope">
          @for (s of SCOPE; track s) {
            <li>{{ t(s) }}</li>
          }
        </ul>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">{{ t('help.more') }}</div>
        <div class="row wrap" style="gap: 8px; margin-top: 10px">
          <a class="btn btn-outline btn-sm" routerLink="/api-docs">{{ t('help.api') }}</a>
          <a class="btn btn-outline btn-sm" routerLink="/usage">{{ t('help.usage') }}</a>
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
  protected readonly t = t;
  protected readonly SYNTAX = [
    { codeKey: 'help.syntax.plus.code', descKey: 'help.syntax.plus.desc' },
    { codeKey: 'help.syntax.minus.code', descKey: 'help.syntax.minus.desc' },
    { codeKey: 'help.syntax.phrase.code', descKey: 'help.syntax.phrase.desc' },
    { codeKey: 'help.syntax.site.code', descKey: 'help.syntax.site.desc' },
  ];

  protected readonly SCOPE = ['help.scope.1', 'help.scope.2', 'help.scope.3', 'help.scope.4'];
}
