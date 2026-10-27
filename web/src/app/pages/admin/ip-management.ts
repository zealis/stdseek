import { Component, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { fullTime } from '../../shared/format';
import { Confirm } from '../../shared/ui/confirm';
import { AdminToolbar } from './admin-toolbar';
import { type GeoInfo, type GeoRiskRow } from '../../core/geo-types';
import { Modal } from '../../shared/ui/modal';
import { Icon } from '../../shared/ui/icon';



type IPBan = {
  id: number;
  pattern: string;
  reason: string;
  created_at: number;
  expires_at: number;
};

const RISK_THRESHOLD = 3;

/**
 * IP 管理：高风险 IP 聚合 + 封禁规则。
 *
 * 高风险的定义：同一个 IP 上活动过 3 个以上账号（阈值在后端）。
 * 这类 IP 十有八九是批量注册的机器号，值得看一眼再决定封不封——
 * 所以列表只排序不自动封，封禁要人工确认。
 */
@Component({
  selector: 'vx-ip-management',
  imports: [AdminToolbar, Confirm, FormsModule, Modal, Icon],
  template: `
    <vx-admin-toolbar title="IP 管理">
      <button type="button" class="btn btn-solid" (click)="banOpen.set(true)">
        <vx-icon name="plus" [size]="14" />
        添加封禁
      </button>
    </vx-admin-toolbar>

    <div class="card" style="margin-bottom: 12px">
      <div class="section-title">归属地解析</div>
      <div class="row wrap" style="margin-top: 10px; gap: 8px; align-items: flex-end">
        <label class="field" style="flex: 1; min-width: 280px">
          <span>解析接口（URL 含 {{ "{" }}ip{{ "}" }}）</span>
          <input
            class="input mono"
            [placeholder]="'https://ip-api.com/json/' + '{' + 'ip' + '}?fields=country,regionName,city,isp'"
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
          <input
            class="input mono"
            placeholder="8.8.8.8"
            [value]="probeIP()" (input)="probeIP.set($any($event.target).value)"
          />
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
      </div>
      @if (geoReady()) {
        <p class="faint small" style="margin-top: 8px">已启用。解析结果会缓存，同一 IP 只请求一次外部接口</p>
      }
      @if (geoErr()) { <p class="small" style="margin-top: 8px; color: var(--danger)">{{ geoErr() }}</p> }
    </div>

    @if (err()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ err() }}</div>
    }

    <div class="card" style="padding: 0; margin-bottom: 12px">
      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">高风险 IP</span>
            <span class="chip">同一 IP ≥ {{ threshold() }} 个账号</span>
          </div>
        </div>
      </div>
      @for (r of risk(); track r.ip) {
        <div class="flag">
          <div class="flag-body">
            <div class="row wrap">
              <span class="mono cell-strong">{{ r.ip }}</span>
              @if (r.accounts >= threshold()) {
                <span class="chip chip-danger">高风险</span>
              }
              <span class="chip">关联 {{ r.accounts }} 个账号 · {{ r.logins }} 次登录</span>
            </div>
            <p class="flag-foot">
              {{ r.label || '归属地未知' }}{{ r.isp ? ' · ' + r.isp : '' }} · ASN {{ r.asn || '—' }} · 最近活动 {{ fullTime(r.last_seen) }}
            </p>
            @if ((r.score ?? 0) > 0) {
              <div class="row wrap" style="gap: 4px; margin-top: 4px">
                <span class="chip" [class]="scoreTone(r.score) === 'bad' ? 'chip-danger' : (scoreTone(r.score) === 'warn' ? 'chip-warning' : 'chip-success')">
                  信誉分 {{ r.score }}{{ r.status ? ' · ' + r.status : '' }}
                </span>
                @for (tag of riskTags(r); track tag) {
                  <span class="chip chip-danger">{{ tag }}</span>
                }
              </div>
            }
          </div>
          <div class="row" style="gap: 6px">
            <button type="button" class="btn btn-link btn-sm" (click)="viewAccounts(r.ip)">账号</button>
            <button type="button" class="btn btn-outline btn-sm" (click)="ban(r.ip)">封禁</button>
          </div>
        </div>
      } @empty {
        <div style="padding: 24px" class="faint small">还没有登录记录</div>
      }
    </div>

    <div class="card" style="padding: 0">
      <div class="flag">
        <div class="flag-body">
          <span class="section-title">封禁规则</span>
        </div>
      </div>
      @for (b of bans(); track b.id) {
        <div class="flag">
          <div class="flag-body">
            <div class="row wrap">
              <span class="mono cell-strong">{{ b.pattern }}</span>
              <span class="chip">{{ b.expires_at === 0 ? '永久' : '到期 ' + fullTime(b.expires_at) }}</span>
            </div>
            <p class="flag-foot">{{ b.reason || '（未填原因）' }}</p>
          </div>
          <button type="button" class="btn btn-link btn-sm" (click)="askRemove(b)">解封</button>
        </div>
      } @empty {
        <div style="padding: 24px" class="faint small">暂无封禁规则</div>
      }
    </div>

    @if (banOpen()) {
      <vx-modal title="添加 IP 封禁" [width]="420" (closed)="banOpen.set(false)">
        <label class="field">
          <span>IP 或通配</span>
          <input class="input mono" placeholder="1.2.3.4 或 1.2.3.*" [(ngModel)]="banPattern" name="p" />
        </label>
        <label class="field">
          <span>原因</span>
          <input class="input" placeholder="例如：批量注册" [(ngModel)]="banReason" name="r" />
        </label>
        <label class="field">
          <span>封禁天数（0 = 永久）</span>
          <input class="input mono" inputmode="numeric" [(ngModel)]="banDays" name="d" />
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
            @if (a.status !== 1) {
              <span class="chip chip-danger">已禁用</span>
            }
          </div>
        } @empty {
          <p class="faint small">没有账号记录</p>
        }
      </vx-modal>
    }

    @if (confirmIP()) {
      <vx-confirm
        title="封禁该 IP"
        desc="封禁后，来自该 IP 的所有 API 请求（登录、注册、提交、反馈）都会返回 403。"
        confirmText="封禁"
        [danger]="true"
        (confirmed)="doBan()"
        (cancelled)="confirmIP.set('')"
      />
    }
  `,
})
export class IPManagementPage {
  private readonly api = inject(AdminApi);

  protected readonly fullTime = fullTime;
  protected readonly risk = signal<GeoRiskRow[]>([]);
  protected readonly geoAPI = signal('');
  protected readonly geoReady = signal(false);
  protected readonly geoBusy = signal(false);
  protected readonly probeIP = signal('');
  protected readonly probeBusy = signal(false);
  protected readonly probeResult = signal<GeoInfo | null>(null);
  protected readonly probeErr = signal('');
  protected readonly ipAccounts = signal<{ ip: string; users: { id: number; email: string; role: string; status: number }[] } | null>(null);
  protected readonly geoErr = signal('');
  protected readonly bans = signal<IPBan[]>([]);
  protected readonly threshold = signal(RISK_THRESHOLD);
  protected readonly loading = signal(true);
  protected readonly err = signal('');

  protected readonly banOpen = signal(false);
  protected readonly banPattern = signal('');
  protected readonly banReason = signal('');
  protected readonly banDays = signal('0');
  protected readonly busy = signal(false);
  protected readonly confirmIP = signal('');
  protected readonly pendingBan = signal<{ pattern: string; reason: string; days: number } | null>(null);

  constructor() {
    void this.load();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    this.err.set('');
    try {
      const [risk, bans, geo] = await Promise.all([
        this.api.geoRisk(),
        this.api.ipBans(),
        this.api.getGeo(),
      ]);
      this.risk.set(risk.items ?? []);
      this.threshold.set(risk.threshold ?? RISK_THRESHOLD);
      this.bans.set(bans.items ?? []);
      this.geoAPI.set(geo.api ?? '');
      this.geoReady.set(!!geo.ready);
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected ban(ip: string): void {
    this.banPattern.set(ip);
    this.banReason.set('');
    this.banOpen.set(true);
  }

  protected async saveBan(): Promise<void> {
    const pattern = this.banPattern().trim();
    if (!pattern) return;
    this.pendingBan.set({
      pattern,
      reason: this.banReason().trim(),
      days: Number(this.banDays()) || 0,
    });
    this.confirmIP.set(pattern);
    this.banOpen.set(false);
  }

  protected async doBan(): Promise<void> {
    const p = this.pendingBan();
    this.confirmIP.set('');
    if (!p) return;
    this.busy.set(true);
    try {
      await this.api.addIPBan(p.pattern, p.reason, p.days);
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set(false);
    }
  }

  /** 手工解析一个 IP，用来验证接口配置是否可用。 */
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

  /** 信誉分越高越好：≥80 绿、50–79 黄、<50 红。 */
  protected scoreTone(score: number | undefined): 'ok' | 'warn' | 'bad' {
    const v = score ?? 0;
    if (v >= 80) return 'ok';
    if (v >= 50) return 'warn';
    return 'bad';
  }

  /** 查看某个 IP 上用过的账号（用户画像入口）。 */
  protected async viewAccounts(ip: string): Promise<void> {
    this.ipAccounts.set({ ip, users: [] });
    try {
      const r = await this.api.accountsByIP(ip);
      this.ipAccounts.set({ ip, users: r.items ?? [] });
    } catch {
      /* 读不到就保持空 */
    }
  }

  protected riskTags(r: GeoRiskRow): string[] {
    try {
      const arr = JSON.parse(r.risk || '[]');
      return Array.isArray(arr) ? arr : [];
    } catch {
      return [];
    }
  }

  protected async askRemove(b: IPBan): Promise<void> {
    this.confirmTarget.set(b);
  }

  protected readonly confirmTarget = signal<IPBan | null>(null);

  protected async removeBan(): Promise<void> {
    const b = this.confirmTarget();
    this.confirmTarget.set(null);
    if (!b) return;
    try {
      await this.api.removeIPBan(b.id);
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    }
  }
}
