import { Component, inject, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { PublicApi } from '../core/public-api.service';
import { type Status } from '../core/api';
import { Icon } from '../shared/ui/icon';

const EXAMPLES = ['crashloopbackoff', 'gse 分词', 'hamming distance'];

/**
 * 首页。运行状态挂载后取：首屏先把搜索框交出去，统计数字后到不阻塞输入。
 * 空索引是最常见的首部署状态——不给引导的话，用户第一反应是「这东西坏了」。
 */
@Component({
  selector: 'vx-home-page',
  imports: [Icon, RouterLink],
  template: `
    <div class="page hero">
      <h1 class="hero-title">
        <span class="hero-grad">voidxo</span> <span class="brand-sub">维空</span>
      </h1>
      <p class="hero-desc">只索引人工审核过的技术站点，每条结果附排序理由。</p>

      <form class="searchbar hero-form" (submit)="submit($event)" role="search">
        <input
          class="searchbar-input"
          type="search"
          enterkeyhint="search"
          placeholder="报错文本、API 名、版本号"
          aria-label="搜索关键词"
          autofocus
          [value]="q()"
          (input)="q.set(inputEl.value)"
          #inputEl
        />
        <button type="submit" class="btn btn-solid btn-lg">
          <vx-icon name="search" [size]="16" />
          搜索
        </button>
      </form>

      @if (empty()) {
        <div class="notice guide">
          <div class="notice-body">
            <div class="section-title">索引还是空的</div>
            <p class="muted" style="margin-top: 4px">还没有抓取任何内容，先录入站点再执行一次抓取。</p>
            <div class="row" style="margin-top: 10px">
              <a class="btn btn-solid btn-sm" routerLink="/manage/whitelist">去后台加站点</a>
              <span class="faint small">或启动时加 -crawl-once</span>
            </div>
          </div>
        </div>
      } @else {
        <div class="hero-examples">
          @for (s of examples; track s) {
            <a class="btn btn-ghost btn-sm" [routerLink]="'/search'" [queryParams]="{ q: s }">
              {{ s }}
            </a>
          }
        </div>
      }

      @if (st(); as s) {
        @if (!empty()) {
          <p class="hero-stat">已收录 {{ s.documents }} 篇文档，来自 {{ s.sites }} 个站点</p>
        }
      }
    </div>
  `,
})
export class HomePage {
  private readonly api = inject(PublicApi);
  private readonly router = inject(Router);

  protected readonly examples = EXAMPLES;
  protected readonly q = signal('');
  protected readonly st = signal<Status | null>(null);

  // 空库时首页会显示引导卡片，提示去后台加站点或跑一次抓取
  protected readonly empty = signal(false);

  constructor() {
    void this.api.status().then((s) => {
      this.st.set(s);
      this.empty.set(s.documents === 0);
    });
  }

  protected submit(event: Event): void {
    event.preventDefault();
    const v = this.q().trim();
    if (v) void this.router.navigate(['/search'], { queryParams: { q: v } });
  }
}
