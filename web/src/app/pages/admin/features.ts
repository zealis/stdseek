import { Component, effect, inject, signal } from '@angular/core';
import { AdminApi } from '../../core/admin-api.service';
import { errMsg } from '../../core/http';
import { fullTime } from '../../shared/format';
import { type FeatureFlag } from '../../core/api';
import { Modal } from '../../shared/ui/modal';

/**
 * 功能开关：只做「改这个开关」一件事。刻意不做「保存」按钮——开关这类东西，
 * 多一次点击就多一次"以为改了其实没改"，点下去即生效，状态以服务端返回为准。
 */
@Component({
  selector: 'vx-features-page',
  imports: [Modal],
  template: `
    @if (err()) {
      <div class="notice notice-error" style="margin-bottom: 12px">{{ err() }}</div>
    }
    @if (msg()) {
      <div class="notice notice-success" style="margin-bottom: 12px">{{ msg() }}</div>
    }

    <div class="card" style="padding: 0">
      @if (loading()) {
        <div class="loading"><span class="spinner spinner-lg"></span></div>
      }
      @if (!loading() && items().length === 0) {
        <div style="padding: 32px; text-align: center" class="faint">没有可配置的开关</div>
      }
      @for (f of items(); track f.key) {
        <div class="flag">
          <div class="flag-body">
            <div class="row wrap">
              <span class="section-title">{{ f.label }}</span>
              @if (f.danger) {
                <span class="chip chip-warning">涉及模型调用成本</span>
              }
              <span class="chip" [class]="f.enabled ? 'chip-success' : ''">
                {{ f.enabled ? '运行中' : '已关闭' }}
              </span>
            </div>
            <p class="flag-desc">{{ f.desc }}</p>
            <p class="flag-foot">
              {{ f.updated_at ? '最后修改 ' + fullTime(f.updated_at) : '从未修改过，当前是默认值' }}
            </p>
          </div>
          <button
            type="button"
            class="switch"
            role="switch"
            [attr.aria-checked]="f.enabled"
            [attr.aria-label]="f.label"
            [class.is-on]="f.enabled"
            [disabled]="busy() === f.key"
            (click)="toggle(f, !f.enabled)"
          ></button>
        </div>
      }
    </div>

    <!-- 配置类项：一行一个，点「配置」开弹窗。表单常驻会把开关列表淹掉。 -->
    <div class="card" style="margin-top: 12px; padding: 0">
      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">邮件（SMTP）</span>
            <span class="chip" [class.chip-success]="mailEnabled()">
              {{ mailEnabled() ? '已配置' : '未配置' }}
            </span>
          </div>
          <p class="flag-foot">
            {{ mailEnabled() ? mailHost() : '未配置时验证与重置邮件只写进服务端日志' }}
          </p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="mailOpen.set(true)">配置</button>
      </div>

      <div class="flag">
        <div class="flag-body">
          <div class="row wrap">
            <span class="section-title">人机验证方式</span>
            <span class="chip" [class.chip-success]="capProvider() !== 'none' && capProvider() !== 'math'">
              {{ capLabel() }}
            </span>
          </div>
          <p class="flag-foot">{{ capFoot() }}</p>
        </div>
        <button type="button" class="btn btn-outline btn-sm" (click)="capOpen.set(true)">配置</button>
      </div>
    </div>

    @if (mailOpen()) {
      <vx-modal title="邮件（SMTP）" [width]="520" (closed)="mailOpen.set(false)">
        <div class="row wrap" style="gap: 10px">
          <label class="field" style="flex: 2; min-width: 220px">
            <span>SMTP 服务器</span>
            <input class="input" placeholder="smtp.qq.com"
              [value]="mailHost()" (input)="mailHost.set($any($event.target).value)" />
          </label>
          <label class="field" style="width: 110px">
            <span>端口</span>
            <input class="input mono" inputmode="numeric"
              [value]="mailPort()" (input)="mailPort.set($any($event.target).value)" />
          </label>
        </div>

        <div class="row wrap" style="gap: 10px; margin-top: 10px">
          <label class="field" style="flex: 1; min-width: 180px">
            <span>用户名</span>
            <input class="input" autocomplete="off"
              [value]="mailUser()" (input)="mailUser.set($any($event.target).value)" />
          </label>
          <label class="field" style="flex: 1; min-width: 180px">
            <span>密码{{ mailHasPass() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ mailHasPass() ? '••••••••' : '' }}"
              [value]="mailPass()" (input)="mailPass.set($any($event.target).value)" />
          </label>
        </div>

        <label class="field" style="margin-top: 10px">
          <span>发件地址</span>
          <input class="input" placeholder="留空则用用户名"
            [value]="mailFrom()" (input)="mailFrom.set($any($event.target).value)" />
        </label>

        <label class="row" style="gap: 6px; align-items: center; margin-top: 10px; cursor: pointer">
          <input type="checkbox" [checked]="mailSecure()"
            (change)="mailSecure.set($any($event.target).checked)" />
          <span class="small">使用隐式 TLS（465 端口）</span>
        </label>

        <div class="row wrap" style="margin-top: 14px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="mailBusy()" (click)="saveMail()">
            @if (mailBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
          @if (mailMsg()) { <span class="small" style="color: var(--success)">{{ mailMsg() }}</span> }
          @if (mailErr()) { <span class="small" style="color: var(--danger)">{{ mailErr() }}</span> }
        </div>

        <div class="row wrap" style="margin-top: 14px; gap: 8px; align-items: flex-end;
             border-top: 1px solid var(--line); padding-top: 14px">
          <label class="field" style="flex: 1; min-width: 200px">
            <span>测试收件地址</span>
            <input class="input" type="email" placeholder="you@example.com"
              [value]="testTo()" (input)="testTo.set($any($event.target).value)" />
          </label>
          <button type="button" class="btn btn-outline" [disabled]="testBusy()" (click)="sendTestMail()">
            @if (testBusy()) { <span class="spinner"></span> } @else { 发送测试邮件 }
          </button>
        </div>
        @if (testMsg()) { <p class="small" style="margin-top: 8px; color: var(--success)">{{ testMsg() }}</p> }
        @if (testErr()) { <p class="small" style="margin-top: 8px; color: var(--danger)">{{ testErr() }}</p> }
      </vx-modal>
    }

    @if (capOpen()) {
      <vx-modal title="人机验证方式" [width]="480" (closed)="capOpen.set(false)">
        <div class="row wrap" style="gap: 6px">
          @for (p of PROVIDERS; track p.value) {
            <button
              type="button"
              class="chip"
              [class.is-active]="capProvider() === p.value"
              (click)="capProvider.set(p.value)"
            >
              {{ p.label }}
            </button>
          }
        </div>

        @if (needsKeys()) {
          <label class="field" style="margin-top: 14px">
            <span>{{ siteKeyLabel() }}</span>
            <input class="input"
              [value]="capSiteKey()" (input)="capSiteKey.set($any($event.target).value)" />
          </label>
          <label class="field" style="margin-top: 10px">
            <span>{{ secretLabel() }}{{ capHasSecret() ? '（留空则不修改）' : '' }}</span>
            <input class="input" type="password" autocomplete="off"
              placeholder="{{ capHasSecret() ? '••••••••' : '' }}"
              [value]="capSecret()" (input)="capSecret.set($any($event.target).value)" />
          </label>
        }

        @if (capProvider() === 'cap') {
          <label class="field" style="margin-top: 10px">
            <span>Cap 服务地址</span>
            <input class="input"
              [value]="capEndpoint()" (input)="capEndpoint.set($any($event.target).value)" />
          </label>
        }

        <div class="row wrap" style="margin-top: 16px; gap: 8px; align-items: center">
          <button type="button" class="btn btn-solid btn-sm" [disabled]="capBusy()" (click)="saveCaptcha()">
            @if (capBusy()) { <span class="spinner"></span> } @else { 保存 }
          </button>
          <button type="button" class="btn btn-outline btn-sm" [disabled]="capTestBusy()" (click)="testCaptcha()">
            @if (capTestBusy()) { <span class="spinner"></span> } @else { 测试连通性 }
          </button>
          @if (capMsg()) { <span class="small" style="color: var(--success)">{{ capMsg() }}</span> }
          @if (capErr()) { <span class="small" style="color: var(--danger)">{{ capErr() }}</span> }
        </div>
      </vx-modal>
    }
  `,
})
export class FeaturesPage {
  // ---- 邮件（SMTP）----
  protected readonly mailOpen = signal(false);
  protected readonly capOpen = signal(false);
  protected readonly mailEnabled = signal(false);
  protected readonly mailHost = signal('');
  protected readonly mailPort = signal('587');
  protected readonly mailUser = signal('');
  protected readonly mailPass = signal('');
  protected readonly mailFrom = signal('');
  protected readonly mailSecure = signal(false);
  protected readonly mailHasPass = signal(false);
  protected readonly mailBusy = signal(false);
  protected readonly mailMsg = signal('');
  protected readonly mailErr = signal('');
  protected readonly testTo = signal('');
  protected readonly testBusy = signal(false);
  protected readonly testMsg = signal('');
  protected readonly testErr = signal('');

  private async loadMail() {
    try {
      const c = await this.api.getMailConfig();
      this.mailHost.set(c.host ?? '');
      this.mailPort.set(String(c.port || 587));
      this.mailUser.set(c.user ?? '');
      this.mailFrom.set(c.from ?? '');
      this.mailSecure.set(!!c.secure);
      this.mailHasPass.set(!!c.has_pass);
      this.mailEnabled.set(!!c.enabled);
      this.mailPass.set('');
    } catch {
      /* 读不到保持默认 */
    }
  }

  protected async saveMail(): Promise<void> {
    this.mailMsg.set('');
    this.mailErr.set('');
    this.mailBusy.set(true);
    try {
      await this.api.setMailConfig({
        host: this.mailHost().trim(),
        port: Number(this.mailPort()) || 0,
        user: this.mailUser().trim(),
        pass: this.mailPass().trim(),
        from: this.mailFrom().trim(),
        secure: this.mailSecure(),
      });
      this.mailHasPass.update((v) => v || this.mailPass().trim() !== '');
      this.mailPass.set('');
      this.mailEnabled.set(this.mailHost().trim() !== '');
      this.mailMsg.set('已保存');
    } catch (e) {
      this.mailErr.set(errMsg(e));
    } finally {
      this.mailBusy.set(false);
    }
  }

  protected async sendTestMail(): Promise<void> {
    this.testMsg.set('');
    this.testErr.set('');
    this.testBusy.set(true);
    try {
      const r = await this.api.testMail(this.testTo().trim());
      this.testMsg.set(r.message);
    } catch (e) {
      this.testErr.set(errMsg(e));
    } finally {
      this.testBusy.set(false);
    }
  }

  /** 行内展示的 provider 名称。 */
  protected capLabel(): string {
    const hit = this.PROVIDERS.find((p) => p.value === this.capProvider());
    return hit ? hit.label : '关闭';
  }

  /** 行内说明：说清当前状态的实际后果，不是解释规则。 */
  protected capFoot(): string {
    const p = this.capProvider();
    if (p === 'none' || p === '') return '未选择验证方式';
    if (p === 'math') return '内置算术题，零配置';
    return this.capHasSecret() ? '已配置密钥' : '缺少密钥，暂不生效';
  }

  protected readonly PROVIDERS: { value: string; label: string }[] = [
    { value: 'none', label: '关闭' },
    { value: 'math', label: '内置算术题' },
    { value: 'turnstile', label: 'Cloudflare Turnstile' },
    { value: 'geetest', label: '极验 Geetest v4' },
    { value: 'vaptcha', label: 'VAPTCHA' },
    { value: 'cap', label: 'Cap（自托管）' },
  ];

  protected readonly capProvider = signal('none');
  protected readonly capSiteKey = signal('');
  protected readonly capSecret = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly capHasSecret = signal(false);
  protected readonly capBusy = signal(false);
  protected readonly capMsg = signal('');
  protected readonly capTestBusy = signal(false);
  protected readonly capErr = signal('');

  protected needsKeys(): boolean {
    const p = this.capProvider();
    return p !== 'none' && p !== 'math';
  }

  protected siteKeyLabel(): string {
    switch (this.capProvider()) {
      case 'geetest':
        return 'Captcha ID';
      case 'vaptcha':
        return 'VID';
      default:
        return 'Site Key';
    }
  }

  protected secretLabel(): string {
    switch (this.capProvider()) {
      case 'geetest':
        return 'Captcha Key';
      case 'vaptcha':
        return 'Secret Key';
      default:
        return 'Secret Key';
    }
  }

  private async loadCaptcha() {
    try {
      const c = await this.api.getCaptchaConfig();
      this.capProvider.set(c.provider || 'none');
      this.capSiteKey.set(c.site_key ?? '');
      this.capEndpoint.set(c.endpoint ?? '');
      this.capHasSecret.set(!!c.has_secret);
      this.capSecret.set('');
    } catch {
      /* 读不到就保持默认，不打扰 */
    }
  }

  protected async testCaptcha(): Promise<void> {
    this.capMsg.set('');
    this.capErr.set('');
    this.capTestBusy.set(true);
    try {
      const r = await this.api.testCaptcha();
      this.capMsg.set(r.message);
    } catch (e) {
      this.capErr.set(errMsg(e));
    } finally {
      this.capTestBusy.set(false);
    }
  }

  protected async saveCaptcha(): Promise<void> {
    this.capMsg.set('');
    this.capErr.set('');
    this.capBusy.set(true);
    try {
      const r = await this.api.setCaptchaConfig({
        provider: this.capProvider(),
        site_key: this.capSiteKey().trim(),
        secret: this.capSecret().trim(),
        endpoint: this.capEndpoint().trim(),
      });
      this.capHasSecret.update((v) => v || this.capSecret().trim() !== '');
      this.capSecret.set('');
      this.capMsg.set(r.ready ? '已保存并生效' : '已保存，但 key 不完整，暂不生效');
    } catch (e) {
      this.capErr.set(errMsg(e));
    } finally {
      this.capBusy.set(false);
    }
  }

  private readonly api = inject(AdminApi);

  protected readonly items = signal<FeatureFlag[]>([]);
  protected readonly loading = signal(false);
  protected readonly err = signal('');
  protected readonly msg = signal('');
  protected readonly busy = signal('');
  protected readonly fullTime = fullTime;

  constructor() {
    effect(() => {
      void this.load();
    });
    void this.loadCaptcha();
    void this.loadMail();
  }

  private async load(): Promise<void> {
    this.loading.set(true);
    try {
      const r = await this.api.listFeatures();
      this.items.set(r.features ?? []);
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.loading.set(false);
    }
  }

  protected async toggle(f: FeatureFlag, on: boolean): Promise<void> {
    this.busy.set(f.key);
    this.err.set('');
    this.msg.set('');
    try {
      await this.api.setFeature(f.key, on);
      this.msg.set(`${f.label}已${on ? '开启' : '关闭'}，立即生效。`);
      await this.load();
    } catch (e) {
      this.err.set(errMsg(e));
    } finally {
      this.busy.set('');
    }
  }
}
