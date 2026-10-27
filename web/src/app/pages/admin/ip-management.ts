import { Component, computed, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { fullTime } from '../../shared/format';
import { Icon } from '../../shared/ui/icon';
import { Modal } from '../../shared/ui/modal';
import { Pager } from '../../shared/ui/pager';
import { Confirm } from '../../shared/ui/confirm';
import { AdminToolbar, type BatchAction } from './admin-toolbar';
import { type GeoInfo, type GeoRiskRow, type IPBan } from '../../core/geo-types';

const SIZE = 20;

/**
 * IP 管理：高风险 IP 列表 + 封禁规则 + 归属地解析配置。
 *
 * 高风险 = 同一个 IP 上活动过 2 个以上账号。列表只排序不自动封，
 * 封禁要人工确认——误封的代价比漏封高。
 */
@Component({
  selector: 'vx-ip-management',
  imports: [AdminToolbar, Pager, Modal, Icon, Confirm],
  template: `
    @if (err()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ err() }}</div>
    }

    <vx-admin-toolbar
      title="IP 管理"
      [count]="selected().length"
      [actions]="batchActions"
      (kwSearch)="search($event)"
    >
      <span class="faint small">共 {{ total() }} 个</span>
      <button type="button" class="btn btn-solid" style="margin-left: 10px" (click)="banOpen.set(true)">
        <vx-icon name="plus" [size]="14" />
        添加封禁
      </button>
    </vx-admin-toolbar>

    @if (loading()) {
      <div class="loading"><span class="spinner spinner-lg"></span></div>
    } @else {
      <div class="table-wrap">
        <table class="table">
          <thead>
            <tr>
              <th class="table-check">
                <input type="checkbox" aria-label="全选" [checked]="allChecked()" (change)="toggleAll(!allChecked())" />
              </th>
              <th style="width: 15%">IP</th>
              <th style="width: 24%">归属地</th>
              <th style="width: 10%" class="is-right">信誉分</th>
              <th style="width: 12%">风险标记</th>
              <th style="width: 9%" class="is-right">账号</th>
              <th style="width: 15%">最近活动</th>
              <th class="is-right" style="width: 15%">操作</th>
            </tr>
          </thead>
          <tbody>
            @for (r of rows(); track r.ip) {
              <tr [class.is-selected]="selected().includes(r.ip)">
                <td class="table-check" data-nolabel>
                  <input
                    type="checkbox"
                    [attr.aria-label]="'选择 ' + r.ip"
                    [checked]="selected().includes(r.ip)"
                    (change)="toggleOne(r.ip)"
                  />
                </td>
                <td data-label="IP">
                  <span class="cell-strong mono">{{ r.ip }}</span>
                  @if (r.accounts >= threshold()) {
                    <span class="chip chip-danger" style="margin-left: 6px">高风险</span>
                  }
                </td>
                <td data-label="归属地">
                  <span class="small">{{ r.label || '未知' }}</span>
                  @if (r.isp || r.asn) {
                    <span class="small muted" style="display: block">
                      {{ r.isp }}{{ r.asn ? ' · ASN ' + r.asn : '' }}
                    </span>
                  }
                </td>
                <td data-label="信誉分" class="is-right">
                  @if (r.score) {
                    <span [style.color]="scoreColor(r.score)">{{ r.score }}</span>
                  } @else {
                    <span class="muted">—</span>
                  }
                </td>
                <td data-label="风险标记">
                  @for (tag of riskTags(r); track tag) {
                    <span class="chip chip-danger">{{ tag }}</span>
                  } @empty {
                    <span class="muted small">—</span>
                  }
                </td>
                <td data-label="账号" class="is-right">
                  <span class="cell-strong">{{ r.accounts }}</span>
                  <span class="muted small" style="display: block">{{ r.logins }} 次</span>
                </td>
                <td data-label="最近活动"><span class="small muted">{{ fullTime(r.last_seen) }}</span></td>
                <td class="is-actions" data-nolabel>
                  <button type="button" class="btn btn-link btn-sm" (click)="viewAccounts(r.ip)">账号</button>
                  <button type="button" class="btn btn-link btn-sm" (click)="ban(r.ip)">封禁</button>
                </td>
              </tr>
            } @empty {
              <tr>
                <td colspan="8">
                  <div class="empty">
                    <p class="empty-title">暂无高风险 IP</p>
                    <p class="empty-desc">同一个 IP 上出现过 2 个以上账号时才会出现在这里</p>
                  </div>
                </td>
              </tr>
            }
          </tbody>
        </table>
      </div>

      <vx-pager [page]="page()" [pages]="pages()" [total]="total()" (pageChange)="goPage($event)" />
    }

    <div class="section-title" style="margin: 22px 0 8px">封禁规则（{{ bans().length }}）</div>
    <div class="table-wrap">
      <table class="table">
        <thead>
          <tr>
            <th style="width: 24%">规则</th>
            <th style="width: 34%">原因</th>
            <th style="width: 22%">到期</th>
            <th class="is-right" style="width: 20%">操作</th>
          </tr>
        </thead>
        <tbody>
          @for (b of bans(); track b.id) {
            <tr>
              <td data-label="规则"><span class="cell-strong mono">{{ b.pattern }}</span></td>
              <td data-label="原因"><span class="small">{{ b.reason || '—' }}</span></td>
              <td data-label="到期">
                <span class="small muted">{{ b.expires_at === 0 ? '永久' : fullTime(b.expires_at) }}</span>
              </td>
              <td class="is-actions" data-nolabel>
                <button type="button" class="btn btn-link btn-sm" (click)="askRemove(b)">解封</button>
              </td>
            </tr>
          } @empty {
            <tr>
              <td colspan="4"><div class="empty"><p class="empty-desc">没有封禁规则</p></div></td>
            </tr>
          }
        </tbody>
      </table>
    </div>

    <div class="card" style="margin-top: 22px">
      <div class="section-title">归属地解析</div>
      <div class="row wrap" style="margin-top: 10px; gap: 8px; align-items: flex-end">
        <label class="field" style="flex: 1; min-width: 280px">
          <span>解析接口（URL 含 <code>{{ '{' }}ip{{ '}' }}</code>）</span>
          <input
            class="input mono"
            [placeholder]="geoPlaceholder()"
            [value]="geoAPI()" (input)="geoAPI.set($any($event.target).value)"
          />
        </label>
        <button type="button" class="btn btn-outline" [disabled]="geoBusy()" (click)="saveGeo()">
          @if (geoBusy()) { <span class="spinner"></span> } @else { 保存 }
        </button>
      </div>

      <div class="row wrap" style="margin-top: 10px; gap: 8px; align-items: flex-end">
        <label class="field" style="flex: 0 0 220px">
          <span>测试解析</span>
          <input class="input mono" placeholder="8.8.8.8"
            [value]="probeIP()" (input)="probeIP.set($any($event.target).value)" />
        </label>
        <button type="button" class="btn btn-outline" [disabled]="probeBusy()" (click)="probe()">
          @if (probeBusy()) { <span class="spinner"></span> } @else { 解析 }
        </button>
        @if (probeResult(); as g) {
          <span class="small">
            {{ g.label || '未知' }}{{ g.isp ? ' · ' + g.isp : '' }}{{ g.asn ? ' · ASN ' + g.asn : '' }}{{ g.score ? ' · 信誉 ' + g.score + '（' + g.status + '）' : '' }}
          </span>
        }
        @if (probeErr()) { <span class="small" style="color: var(--danger)">{{ probeErr() }}</span> }
        @if (geoErr()) { <span class="small" style="color: var(--danger)">{{ geoErr() }}</span> }
      </div>
    </div>

    @if (banOpen()) {
      <vx-modal title="添加 IP 封禁" [width]="420" (closed)="banOpen.set(false)">
        <label class="field">
          <span>IP 或通配</span>
          <input class="input mono" placeholder="1.2.3.4 或 1.2.3.*"
            [value]="banPattern()" (input)="banPattern.set($any($event.target).value)" />
        </label>
        <label class="field">
          <span>原因</span>
          <input class="input" placeholder="例如：批量注册"
            [value]="banReason()" (input)="banReason.set($any($event.target).value)" />
        </label>
        <label class="field">
          <span>封禁天数（0 = 永久）</span>
          <input class="input mono" inputmode="numeric"
            [value]="banDays()" (input)="banDays.set($any($event.target).value)" />
        </label>
        <div footer class="row" style="gap: 8px; justify-content: flex-end">
          <button type="button" class="btn" (click)="banOpen.set(false)">取消</button>
          <button type="button" class="btn btn-solid" [disabled]="busy()" (click)="saveBan()">
            @if (busy()) { <span class="spinner"></span> } @else { 添加 }
          </button>
        </div>
      </vx-modal>
    }

    @if (ipAccounts()) {
      <vx-modal [title]="'IP ' + ipAccounts()!.ip + ' 上的账号'" [width]="460" (closed)="ipAccounts.set(null)">
        @for (a of ipAccounts()!.users; track a.id) {
          <div class="row" style="gap: 8px; align-items: center; padding: 6px 0">
            <span class="mono small">{{ a.email }}</span>
            <span class="chip">{{ a.role === 'admin' ? '管理员' : '普通用户' }}</span>
            @if (a.status !== 1) { <span class="chip chip-danger">已禁用</span> }
          </div>
        } @empty {
          <p class="faint small">没有账号记录</p>
        }
      </vx-modal>
    }

    @if (confirmIP()) {
      <vx-confirm
        title="封禁该 IP"
        [desc]="banConfirmDesc()"
        confirmText="封禁"
        [danger]="true"
        (confirmed)="doBan()"
        (cancelled)="confirmIP.set('')"
      />
    }

    @if (confirmUnban()) {
      <vx-confirm
        title="解除封禁"
        desc="解除后该 IP 可以重新访问所有接口。"
        confirmText="解封"
        [danger]="true"
        (confirmed)="removeBan()"
        (cancelled)="confirmUnban.set(null)"
      />
    }
  `,
  styles: `
    .is-actions {
      text-align: right;
      white-space: nowrap;
    }
  `,
})
export class IPManagementPage {
  private readonly api = inject(AdminApi);

  protected readonly fullTime = fullTime;
  protected readonly rows = signal<GeoRiskRow[]>([]);
  protected readonly bans = signal<IPBan[]>([]);
  protected readonly total = signal(0);
  protected readonly page = signal(1);
  protected readonly kw = signal('');
  protected readonly threshold = signal(3);
  protected readonly loading = signal(true);
  protected readonly err = signal('');
  protected readonly selected = signal<string[]>([]);
  protected readonly busy = signal(false);

  protected readonly geoAPI = signal('');
  protected readonly geoBusy = signal(false);
  protected readonly geoErr = signal('');
  protected readonly probeIP = signal('');
  protected readonly probeBusy = signal(false);
  protected readonly probeResult = signal<GeoInfo | null>(null);
  protected readonly probeErr = signal('');

  protected readonly banOpen = signal(false);
  protected readonly banPattern = signal('');
  protected readonly banReason = signal('');
  protected readonly banDays = signal('0');
  protected readonly confirmIP = signal('');
  protected readonly pendingBan = signal<{ pattern: string; reason: string; days: number } | null>(null);
  protected readonly confirmUnban = signal<IPBan | null>(null);
  protected readonly ipAccounts = signal<{
    ip: string;
    users: { id: number; email: string; role: string; status: number }[];
  } | null>(null);

  protected readonly pages = computed(() => Math.max(1, Math.ceil(this.total() / SIZE)));
  protected readonly allChecked = computed(
    () => this.rows().length > 0 && this.rows().every((r) => this.selected().includes(r.ip)),
  );

  protected readonly batchActions: BatchAction[] = [
    { label: '封禁选中', danger: true, run: () => this.batchBan() },
  ];

  /** 占位符里不能直接写花括号（会被模板当表达式）。 */
  protected readonly geoPlaceholder = computed(
    () => 'https://ip.huzhihui.com/api/ip/health?ip=' + '{' + 'ip' + '}',
  );

  protected readonly banConfirmDesc = computed(() => {
    const p = this.pendingBan();
    const n = p ? p.pattern.split(',').filter((x) => x.trim() !== '').length : 1;
    return n > 1
      ? `将封禁这 ${n} 个 IP，来自它们的登录、注册、提交、反馈请求都会返回 403。`
      : '封禁后，来自该 IP 的所有请求（登录、注册、提交、反馈）都会返回 403。';
  });

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    this.err.set('');
    try {
      const [risk, bans, geo] = await Promise.all([
        this.api.geoRisk(this.kw(), this.page(), SIZE),
        this.api.ipBans(),
        this.api.getGeo(),
      ]);
      this.rows.set(risk.items ?? []);
      this.total.set(risk.total ?? 0);
      this.threshold.set(risk.threshold ?? 3);
      this.bans.set(bans.items ?? []);
      this.geoAPI.set(geo.api ?? '');
      this.selected.set([]);
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected search(kw: string): void {
    this.kw.set(kw.trim());
    this.page.set(1);
    void this.load();
  }

  protected goPage(p: number): void {
    this.page.set(p);
    void this.load();
  }

  protected toggleAll(all: boolean): void {
    this.selected.set(all ? this.rows().map((r) => r.ip) : []);
  }

  protected toggleOne(ip: string): void {
    this.selected.update((list) => (list.includes(ip) ? list.filter((x) => x !== ip) : [...list, ip]));
  }

  protected riskTags(r: GeoRiskRow): string[] {
    try {
      const arr = JSON.parse(r.risk || '[]');
      return Array.isArray(arr) ? arr : [];
    } catch {
      return [];
    }
  }

  /** 信誉分越高越好：≥80 绿、50–79 黄、<50 红。 */
  protected scoreColor(score: number): string {
    if (score >= 80) return 'var(--success)';
    if (score >= 50) return 'var(--warning)';
    return 'var(--danger)';
  }

  protected async viewAccounts(ip: string): Promise<void> {
    this.ipAccounts.set({ ip, users: [] });
    try {
      const r = await this.api.accountsByIP(ip);
      this.ipAccounts.set({ ip, users: r.items ?? [] });
    } catch {
      /* 读不到就保持空 */
    }
  }

  protected ban(ip: string): void {
    this.banPattern.set(ip);
    this.banReason.set('');
    this.banOpen.set(true);
  }

  /** 批量封禁选中的 IP。 */
  protected batchBan(): void {
    const ips = this.selected();
    if (ips.length === 0) return;
    this.pendingBan.set({ pattern: ips.join(','), reason: '批量封禁', days: 0 });
    this.confirmIP.set(ips.join(','));
  }

  protected async saveBan(): Promise<void> {
    const pattern = this.banPattern().trim();
    if (!pattern) return;
    this.pendingBan.set({ pattern, reason: this.banReason().trim(), days: Number(this.banDays()) || 0 });
    this.confirmIP.set(pattern);
    this.banOpen.set(false);
  }

  protected async doBan(): Promise<void> {
    const p = this.pendingBan();
    this.confirmIP.set('');
    if (!p) return;
    this.busy.set(true);
    try {
      // 批量时 pattern 是逗号分隔的多个 IP
      for (const one of p.pattern.split(',')) {
        const t = one.trim();
        if (t) await this.api.addIPBan(t, p.reason, p.days);
      }
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }

  protected askRemove(b: IPBan): void {
    this.confirmUnban.set(b);
  }

  protected async removeBan(): Promise<void> {
    const b = this.confirmUnban();
    this.confirmUnban.set(null);
    if (!b) return;
    try {
      await this.api.removeIPBan(b.id);
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    }
  }

  protected async saveGeo(): Promise<void> {
    this.geoErr.set('');
    this.geoBusy.set(true);
    try {
      await this.api.setGeo(this.geoAPI().trim());
      await this.load();
    } catch (e) {
      this.geoErr.set(errMsg(e));
    } finally {
      this.geoBusy.set(false);
    }
  }

  protected async probe(): Promise<void> {
    const ip = this.probeIP().trim();
    if (!ip) return;
    this.probeErr.set('');
    this.probeResult.set(null);
    this.probeBusy.set(true);
    try {
      this.probeResult.set(await this.api.resolveGeo(ip));
    } catch (e) {
      this.probeErr.set(errMsg(e));
    } finally {
      this.probeBusy.set(false);
    }
  }
}
