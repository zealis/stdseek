import { Component, inject, signal } from '@angular/core';
import { PublicApi } from '../core/public-api.service';
import { t } from '../core/i18n';

/**
 * 隐私政策。
 *
 * 正文按代码里真实发生的行为写，不写场面话：收集了什么、怎么用、不收集什么、
 * 哪些第三方会在什么条件下收到数据，都可以对着 internal/ 里的实现核对。
 *
 * 生效日与修订日由后端给出（/api/privacy）：
 *   created_at = 最早的管理员账号创建时间（服务从那天起运行并产生数据）
 *   updated_at = settings 里的 privacy.updated_at（每次改政策同步一次）
 */
@Component({
  selector: 'vx-privacy-page',
  imports: [],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="section-title" style="font-size: var(--fs-xl)">{{ t('privacy.title') }}</h1>
      <p class="small muted" style="margin: 6px 0 20px">
        {{
          t('privacy.effective', {
            created: date(meta()?.created_at),
            updated: date(meta()?.updated_at),
          })
        }}
      </p>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s1') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s1.1') }}</li>
          <li>{{ t('privacy.s1.2') }}</li>
          <li>{{ t('privacy.s1.3') }}</li>
          <li>{{ t('privacy.s1.4') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s2') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s2.1') }}</li>
          <li>{{ t('privacy.s2.2') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s3') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s3.1') }}</li>
          <li>{{ t('privacy.s3.2') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s4') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s4.lead') }}</li>
          <li>{{ t('privacy.s4.1') }}</li>
          <li>{{ t('privacy.s4.2') }}</li>
          <li>{{ t('privacy.s4.3') }}</li>
          <li>{{ t('privacy.s4.4') }}</li>
          <li>{{ t('privacy.s4.5') }}</li>
          <li>{{ t('privacy.s4.6') }}</li>
          <li>{{ t('privacy.s4.7') }}</li>
          <li>{{ t('privacy.s4.8') }}</li>
          <li>{{ t('privacy.s4.9') }}</li>
          <li>{{ t('privacy.s4.10') }}</li>
          <li>{{ t('privacy.s4.11') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s5') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s5.1') }}</li>
          <li>{{ t('privacy.s5.2') }}</li>
          <li>{{ t('privacy.s5.3') }}</li>
          <li>{{ t('privacy.s5.4') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s6') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s6.1') }}</li>
          <li>{{ t('privacy.s6.2') }}</li>
          <li>{{ t('privacy.s6.3') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s7') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s7.1') }}</li>
          <li>{{ t('privacy.s7.2') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s8') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s8.1') }}</li>
          <li>{{ t('privacy.s8.2') }}</li>
          <li>{{ t('privacy.s8.3') }}</li>
        </ul>
      </section>

      <section class="card" style="margin-bottom: 12px">
        <div class="section-title">{{ t('privacy.s9') }}</div>
        <ul class="privacy-list">
          <li>{{ t('privacy.s9.1') }}</li>
        </ul>
      </section>

      <section class="card">
        <div class="section-title">{{ t('privacy.s10') }}</div>
        <ul class="privacy-list">
          @if (site()?.admin_email) {
            <li>
              {{ t('privacy.s10.email')
              }}<a [href]="'mailto:' + site()!.admin_email">{{ site()!.admin_email }}</a>
            </li>
          } @else {
            <li>{{ t('privacy.s10.missing') }}</li>
          }
        </ul>
      </section>

      <p class="faint small" style="margin-top: 16px">{{ t('privacy.note') }}</p>
    </div>
  `,
  styles: `
    .privacy-list {
      margin: 8px 0 0;
      padding-left: 18px;
    }

    .privacy-list li {
      font-size: var(--fs-sm);
      line-height: 1.7;
      color: var(--text-2);
      margin-bottom: 6px;
    }

    .privacy-list b {
      color: var(--text-1);
      font-weight: 600;
    }
  `,
})
export class PrivacyPage {
  protected readonly t = t;
  private readonly api = inject(PublicApi);
  protected readonly meta = signal<{ created_at: number; updated_at: number } | null>(null);
  protected readonly site = signal<{ name: string; admin_email: string } | null>(null);

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    try {
      this.meta.set(await this.api.privacy());
    } catch {
      /* 读不到就不显示日期 */
    }
    try {
      this.site.set(await this.api.site());
    } catch {
      /* 读不到就不显示邮箱 */
    }
  }

  protected date(ts: number | undefined): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }
}
