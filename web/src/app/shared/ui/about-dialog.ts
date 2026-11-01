import { Component, inject, output, signal } from '@angular/core';
import { Modal } from './modal';
import { PublicApi } from '../../core/public-api.service';
import { BrandingService } from '../../core/branding.service';
import { t } from '../../core/i18n';

/** CHANGELOG.md 经 `tools/gen-changelog.mjs` 转成的一行。 */
type Entry = { version: string; date: string; items: { type: string; text: string }[] };

/** 条目类型 → CSS 类名。中文类名虽然合法，但写进 class 绑定太脆，先映射成英文。 */
const TAG_CLASS: Record<string, string> = {
  新增: 'feat',
  修复: 'fix',
  改进: 'improve',
  安全: 'security',
  重构: 'refactor',
  移除: 'remove',
  文档: 'docs',
};

/**
 * 「关于」弹窗：开源协议与地址、当前版本、收录规模、优缺点、以及完整更新日志。
 *
 * 为什么做成弹窗而不是新页面：它是一次性的「这是什么」答疑，看完就关；
 * 做成页面会多一条路由、一个要维护的标题，而页脚本来就能随手点。
 *
 * 更新日志走 `/changelog.json`（public/ 下的静态文件）而不是打进 JS：
 * 170 个版本有 37KB，而这里一年打开几次，塞进 main 等于给所有人加下载量。
 */
@Component({
  selector: 'vx-about',
  imports: [Modal],
  template: `
    <vx-modal [title]="t('about.title')" [width]="760" (closed)="closed.emit()">
      <p class="small" style="margin: 0 0 6px">{{ t('about.intro') }}</p>
      <p class="faint small" style="margin: 0 0 14px">
        <a [href]="repo" target="_blank" rel="noreferrer">{{ t('about.repo') }}</a>
        · {{ t('about.license') }}
        @if (version()) {
          · <span class="mono">v{{ version() }}</span>
        }
      </p>

      <div class="notice notice-warning" style="margin-bottom: 14px">
        {{ t('about.devWarning') }}
      </div>

      <div class="about-stats">
        <span>
          <strong class="mono">{{ sites() }}</strong>
          {{ t('about.stats.sites') }}
        </span>
        <span>
          <strong class="mono">{{ documents() }}</strong>
          {{ t('about.stats.pages') }}
        </span>
        <span class="faint small">{{ t('about.stats.growing') }}</span>
      </div>

      <!-- 一滚动更新日志就把优缺点收起来：读日志时它已经不相关，留着只占屏 -->
      @if (tucked()) {
        <button type="button" class="about-expand" (click)="tucked.set(false)">
          {{ t('about.expand') }}
        </button>
      }
      <div class="about-cols" [class.is-tucked]="tucked()">
        <div>
          <div class="section-title">{{ t('about.pros') }}</div>
          <ul class="about-list">
            @for (k of PROS; track k) {
              <li>{{ t(k) }}</li>
            }
          </ul>
        </div>
        <div>
          <div class="section-title">{{ t('about.cons') }}</div>
          <ul class="about-list">
            @for (k of CONS; track k) {
              <li>{{ t(k) }}</li>
            }
          </ul>
        </div>
      </div>

      <div class="row-between" style="margin-top: 18px; align-items: baseline">
        <div class="section-title">{{ t('about.updates') }}</div>
        <span class="faint small">{{ t('about.logNote') }}</span>
      </div>

      <!-- 固定高度的窗口：170 个版本全在这里，滚动看，不把弹窗撑成一条长页 -->
      <div class="about-log" (scroll)="onLogScroll($event)">
        @if (loading()) {
          <div class="loading"><span class="spinner"></span></div>
        } @else if (entries().length === 0) {
          <p class="faint small">{{ t('about.empty') }}</p>
        } @else {
          @for (e of entries(); track e.version) {
            <div class="log-row">
              <div class="log-meta">
                <span class="mono">{{ e.version }}</span>
                <span>{{ e.date }}</span>
              </div>
              <ul class="log-items">
                @for (it of e.items; track $index) {
                  <li>
                    <span class="log-tag" [class]="'tag-' + tagClass(it.type)">{{ it.type }}</span>
                    <span>{{ it.text }}</span>
                  </li>
                }
              </ul>
            </div>
          }
        }
      </div>

      <p class="faint small" style="margin-top: 10px">
        {{ t('about.logMore') }}
        <a [href]="repo + '/blob/main/CHANGELOG.md'" target="_blank" rel="noreferrer"
          >CHANGELOG.md</a
        >
      </p>
    </vx-modal>
  `,
})
export class AboutDialog {
  readonly closed = output<void>();

  private readonly api = inject(PublicApi);
  private readonly branding = inject(BrandingService);

  protected readonly t = t;
  protected readonly repo = 'https://github.com/zealis/stdseek';
  protected readonly version = signal(this.branding.current()?.app_version ?? '');
  protected readonly sites = signal(0);
  protected readonly documents = signal(0);
  protected readonly entries = signal<Entry[]>([]);
  protected readonly loading = signal(true);
  /** 一滚动更新日志就收起优缺点（点「展开」可以再打开）。 */
  protected readonly tucked = signal(false);

  /** 优缺点逐条写死在这里：它们是产品判断，不是可配置项。 */
  protected readonly PROS = ['about.pros.1', 'about.pros.2', 'about.pros.3', 'about.pros.4'];
  protected readonly CONS = [
    'about.cons.1',
    'about.cons.2',
    'about.cons.3',
    'about.cons.4',
    'about.cons.5',
  ];

  constructor() {
    void this.branding.load();
    void this.load();
  }

  private async load(): Promise<void> {
    // 两个请求并行：统计数字走接口，更新日志走静态 JSON（构建时由 CHANGELOG.md 生成）。
    const [status, entries] = await Promise.all([
      this.api.status().catch(() => null),
      fetch('/changelog.json')
        .then((r) => (r.ok ? r.json() : []))
        .catch(() => []),
    ]);
    if (status) {
      this.documents.set(status.documents ?? 0);
      this.sites.set(status.sites ?? 0);
    }
    this.entries.set(Array.isArray(entries) ? entries : []);
    this.loading.set(false);
  }

  /**
   * 日志窗口一被滚动就收起优缺点。
   *
   * 用 8px 阈值而不是 >0：触控板与高分屏滚轮常常先给出 1~2px 的抖动，
   * 那点位移不该触发收起——用户还没开始读。
   */
  protected onLogScroll(e: Event): void {
    const el = e.target as HTMLElement;
    if (!this.tucked() && el.scrollTop > 8) this.tucked.set(true);
  }

  protected tagClass(type: string): string {
    return TAG_CLASS[type] ?? 'other';
  }
}
