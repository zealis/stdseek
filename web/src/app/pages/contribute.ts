import { Component, inject, signal } from '@angular/core';
import { NgTemplateOutlet } from '@angular/common';
import { ActivatedRoute } from '@angular/router';
import { PublicApi } from '../core/public-api.service';
import { errMsg } from '../core/http';
import { CATEGORIES, type SubmissionStatus } from '../core/api';
import { fullTime } from '../shared/format';
import { Icon } from '../shared/ui/icon';

// 用户提交站点：进审核队列，不进白名单。管理员在「管理后台 → 审核队列」逐条处理。
const STEPS = ['已提交', '人工审核', '抓取入库'];

function stepIndex(status: string): number {
  if (status === 'approved') return 2; // 已审核通过，等下一次抓取
  if (status === 'rejected') return 1; // 卡在审核这一步
  return 0; // pending：只完成「已提交」
}

const STATUS_TONE: Record<string, string> = {
  pending: 'chip-warning',
  approved: 'chip-success',
  rejected: 'chip-danger',
};

/** 提交站点 + 查询审核进度。支持带 ?ticket= 的分享链接，刷新后仍能看到进度。 */
@Component({
  selector: 'vx-contribute-page',
  imports: [Icon, NgTemplateOutlet],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">提交站点</h1>
      <p class="page-desc">提交后进入审核队列，通过后可被搜索；同一域名重复提交会合并。</p>

      @if (ticket(); as t) {
        <div class="notice notice-success" style="margin-top: 16px">
          <div class="notice-body">已进入审核队列。请保存下方查询码，之后可随时查看进度。</div>
        </div>

        <div class="card" style="margin-top: 12px">
          <span class="faint small">查询码</span>
          <div class="row wrap" style="margin-top: 6px">
            <span class="mono" style="font-size: var(--fs-lg); color: var(--brand); font-weight: 500">
              {{ t }}
            </span>
            <button type="button" class="btn btn-sm" (click)="copyTicket()">
              <vx-icon [name]="copied() ? 'check' : 'copy'" [size]="14" />
              {{ copied() ? '已复制' : '复制' }}
            </button>
          </div>
          <p class="faint small" style="margin-top: 8px">
            查询码只对应这一次提交，凭它可查看审核状态，不需要注册账号。
          </p>
        </div>

        <ng-container [ngTemplateOutlet]="statusCard" />
        <ng-container [ngTemplateOutlet]="againTpl" />
      } @else {
        <div class="card" style="margin-top: 16px">
          <div class="form-grid">
            <label class="field">
              <span class="field-label">网址</span>
              <input
                class="input"
                type="url"
                inputmode="url"
                autocomplete="url"
                placeholder="https://example.com 或 example.com"
                [value]="form().url"
                (input)="patchForm('url', urlEl.value)"
                #urlEl
              />
              <span class="field-hint">填站点首页即可，具体抓取入口由管理员按站点结构配置</span>
            </label>
            <label class="field">
              <span class="field-label">分类</span>
              <select
                class="input select"
                [value]="form().category"
                (change)="patchForm('category', catEl.value)"
                #catEl
              >
                @for (c of categories; track c.value) {
                  <option [value]="c.value" [selected]="c.value === form().category">
                    {{ c.label }}
                  </option>
                }
              </select>
            </label>
            <label class="field">
              <span class="field-label">理由（可选）</span>
              <textarea
                class="input textarea"
                rows="2"
                maxlength="300"
                placeholder="为什么这个站点值得收录？例如「Redis 官方命令参考」"
                [value]="form().reason"
                (input)="patchForm('reason', reasonEl.value)"
                #reasonEl
              ></textarea>
            </label>
            @if (err()) {
              <div class="notice notice-error">{{ err() }}</div>
            }
            <div>
              <button
                type="button"
                class="btn btn-solid"
                [disabled]="!form().url || busy()"
                (click)="submit()"
              >
                {{ busy() ? '提交中' : '提交' }}
              </button>
            </div>
          </div>
        </div>

        <h2 class="section-title" style="margin-top: 24px">查询已有提交</h2>
        <p class="faint small" style="margin-top: 4px; margin-bottom: 10px">
          查询码没保存无法找回。
        </p>
        <form class="row wrap" (submit)="queryStatus($event)">
          <input
            class="input"
            type="text"
            style="max-width: 320px"
            placeholder="vx…"
            [value]="query()"
            (input)="query.set(qEl.value.trim())"
            #qEl
          />
          <button type="submit" class="btn" [disabled]="!query().trim() || stBusy()">
            {{ stBusy() ? '查询中' : '查询' }}
          </button>
        </form>
        @if (stData() || stErr()) {
          <ng-container [ngTemplateOutlet]="statusCard" />
        }
      }

      <ng-template #statusCard>
        <div class="card" style="margin-top: 12px">
          @if (stErr()) {
            <div class="notice notice-error">{{ stErr() }}</div>
          }
          @if (stData(); as d) {
            <div class="row wrap">
              <span class="section-title">{{ d.domain }}</span>
              <span class="chip {{ STATUS_TONE[d.status] || '' }}">{{ d.status_text }}</span>
            </div>
            <div class="steps" style="margin: 12px 0">
              @for (s of STEPS; track s; let i = $index) {
                <span class="row" style="gap: 8px">
                  <span class="step" [class]="stepClass(d.status, i)">
                    <span class="step-dot"></span>
                    {{ d.status === 'rejected' && i === 1 ? '未通过审核' : s }}
                  </span>
                  @if (i < STEPS.length - 1) {
                    <span class="step-line"></span>
                  }
                </span>
              }
            </div>
            <p class="faint small">
              提交于 {{ fullTime(d.created_at) }}{{ d.status === 'approved' ? '，已加入抓取白名单，下一次抓取后可搜索' : '' }}
            </p>
          }
          @if (!stData() && !stErr()) {
            <p class="faint small">{{ stBusy() ? '查询中' : '暂无进度信息' }}</p>
          }
          <div style="margin-top: 12px">
            <button
              type="button"
              class="btn btn-sm"
              [disabled]="stBusy() || !query().trim()"
              (click)="loadStatus()"
            >
              刷新进度
            </button>
          </div>
        </div>
      </ng-template>

      <ng-template #againTpl>
        <div style="margin-top: 12px">
          <button type="button" class="btn btn-ghost btn-sm" (click)="startOver()">再提交一个</button>
        </div>
      </ng-template>
    </div>
  `,
})
export class ContributePage {
  private readonly api = inject(PublicApi);
  private readonly route = inject(ActivatedRoute);

  protected readonly categories = CATEGORIES;
  protected readonly STEPS = STEPS;
  protected readonly STATUS_TONE = STATUS_TONE;
  protected readonly fullTime = fullTime;

  protected readonly form = signal({ url: '', category: 'blog', reason: '' });
  protected readonly busy = signal(false);
  protected readonly err = signal('');
  protected readonly ticket = signal('');
  protected readonly copied = signal(false);

  protected readonly query = signal('');
  protected readonly stData = signal<SubmissionStatus | null>(null);
  protected readonly stErr = signal('');
  protected readonly stBusy = signal(false);

  constructor() {
    // 支持把带查询码的链接直接发给用户（?ticket=vx...），刷新后仍能看到进度。
    const t = this.route.snapshot.queryParamMap.get('ticket');
    if (t) {
      this.query.set(t);
      void this.loadStatus();
    }
  }

  protected patchForm(key: 'url' | 'category' | 'reason', value: string): void {
    this.form.update((f) => ({ ...f, [key]: value.trim() }));
  }

  protected async submit(): Promise<void> {
    this.err.set('');
    this.busy.set(true);
    try {
      const r = await this.api.submitSite({
        url: this.form().url,
        category: this.form().category,
        reason: this.form().reason || undefined,
      });
      this.ticket.set(r.ticket);
      this.query.set(r.ticket);
      this.stData.set(null);
      this.stErr.set('');
      this.form.set({ url: '', category: 'blog', reason: '' });
      await this.loadStatus();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected async loadStatus(): Promise<void> {
    const code = this.query().trim();
    if (!code) return;
    this.stBusy.set(true);
    this.stErr.set('');
    try {
      this.stData.set(await this.api.getSubmission(code));
    } catch (e) {
      this.stData.set(null);
      this.stErr.set(errMsg(e));
    } finally {
      this.stBusy.set(false);
    }
  }

  protected queryStatus(event: Event): void {
    event.preventDefault();
    void this.loadStatus();
  }

  protected stepClass(status: string, i: number): string {
    const rejected = status === 'rejected' && i === 1;
    if (rejected) return 'is-blocked';
    return i <= stepIndex(status) && status !== 'rejected' ? 'is-reached' : '';
  }

  protected async copyTicket(): Promise<void> {
    try {
      await navigator.clipboard.writeText(this.ticket());
      this.copied.set(true);
      setTimeout(() => this.copied.set(false), 2000);
    } catch {
      this.copied.set(false);
    }
  }

  protected startOver(): void {
    this.ticket.set('');
    this.stData.set(null);
    this.stErr.set('');
  }
}
