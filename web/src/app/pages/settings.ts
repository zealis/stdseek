import { Component, computed, inject, signal } from '@angular/core';
import { PrefsService, type Theme } from '../core/prefs.service';
import { parseUA } from '../core/ua';
import { AuthService, type CaptchaChallenge, authErrMsg } from '../core/auth.service';
import {
  passkeyBlockedByIP,
  passkeyErrorText,
  passkeySupported,
  serializeAttestation,
  toCreationOptions,
} from '../core/webauthn';
import { RouterLink } from '@angular/router';
import { DeviceList, type DeviceRow } from '../shared/ui/device-list';
import { CaptchaDialog } from '../shared/ui/captcha-dialog';
import { Confirm } from '../shared/ui/confirm';
import { Modal } from '../shared/ui/modal';
import { FILTERS, TIME_WINDOWS } from '../core/api';
import { Icon } from '../shared/ui/icon';
import { t } from '../core/i18n';

/** 搜索偏好 + 外观。偏好保存在本机，结果页默认按此过滤；主题三选一（DESIGN.md §11）。 */
@Component({
  selector: 'vx-settings-page',
  imports: [Icon, RouterLink, DeviceList, CaptchaDialog, Confirm, Modal],
  template: `
    <div class="page" style="padding-top: 24px; padding-bottom: 48px">
      <h1 class="page-title">{{ t('settings.title') }}</h1>
      <p class="page-desc">{{ t('settings.desc') }}</p>

      <div class="card" style="margin-top: 16px">
        <div class="section-title">{{ t('settings.account') }}</div>

        @if (!auth.ready()) {
          <p class="faint small" style="margin-top: 8px">{{ t('settings.loading') }}</p>
        } @else if (!auth.loggedIn()) {
          <p class="faint small" style="margin-top: 8px">
            {{ t('settings.loggedOut') }}
          </p>
          <div class="row" style="margin-top: 10px; gap: 8px">
            <a class="btn btn-solid btn-sm" routerLink="/login">{{ t('settings.login') }}</a>
            @if (auth.userAuthOpen()) {
              <a class="btn btn-outline btn-sm" routerLink="/register">{{
                t('settings.register')
              }}</a>
            }
          </div>
        } @else {
          <div class="row-between" style="margin-top: 8px">
            <div style="min-width: 0">
              <div class="mono" style="font-size: var(--fs-sm)">{{ user()?.email }}</div>
              <p class="faint small" style="margin-top: 2px">
                {{ user()?.role === 'admin' ? t('settings.role.admin') : t('settings.role.user') }}
                · {{ t('auth.email') }}
                {{
                  user()?.email_verified
                    ? t('settings.email.verified')
                    : t('settings.email.unverified')
                }}
                @if (user()?.totp_enabled) {
                  · {{ t('settings.totp.on') }}
                }
              </p>
            </div>
            <button type="button" class="btn btn-ghost btn-sm" (click)="askLogout()">
              {{ t('settings.logout') }}
            </button>
          </div>

          <div class="row wrap" style="margin-top: 12px; gap: 8px">
            <button type="button" class="btn btn-outline btn-sm" (click)="togglePwd()">
              {{ t('settings.changePassword') }}
            </button>
            <button type="button" class="btn btn-outline btn-sm" (click)="toggleEmail()">
              {{ t('settings.changeEmail') }}
            </button>
            <button type="button" class="btn btn-outline btn-sm" (click)="toggleTotp()">
              {{ user()?.totp_enabled ? t('settings.totp.disable') : t('settings.totp.enable') }}
            </button>
          </div>

          @if (pwdMsg()) {
            <div class="notice notice-accent" style="margin-top: 12px">{{ pwdMsg() }}</div>
          }
          @if (pwdErr()) {
            <div class="notice notice-error" style="margin-top: 12px">{{ pwdErr() }}</div>
          }

          @if (emailMsg()) {
            <div class="notice notice-accent" style="margin-top: 12px">{{ emailMsg() }}</div>
          }
          @if (emailErr()) {
            <div class="notice notice-error" style="margin-top: 12px">{{ emailErr() }}</div>
          }

          @if (emailOpen()) {
            <form
              style="margin-top: 14px; border-top: 1px solid var(--line); padding-top: 14px"
              (submit)="submitEmail($event)"
            >
              <label class="auth-field">
                <span>{{ t('settings.newEmail') }}</span>
                <input
                  class="input"
                  type="email"
                  autocomplete="email"
                  [value]="emailNew()"
                  (input)="emailNew.set($any($event.target).value)"
                />
              </label>
              <label class="auth-field">
                <span>{{ t('settings.currentPassword') }}</span>
                <input
                  class="input"
                  type="password"
                  autocomplete="current-password"
                  [value]="emailPwd()"
                  (input)="emailPwd.set($any($event.target).value)"
                />
              </label>
              <p class="faint small" style="margin: -2px 0 12px">
                {{ t('settings.emailHint') }}
              </p>
              <button type="submit" class="btn btn-solid btn-sm" [disabled]="emailBusy()">
                @if (emailBusy()) {
                  <span class="spinner"></span>
                } @else {
                  {{ t('settings.saveEmail') }}
                }
              </button>
            </form>
          }

          @if (pwdOpen()) {
            <form
              style="margin-top: 14px; border-top: 1px solid var(--line); padding-top: 14px"
              (submit)="submitPwd($event)"
            >
              <label class="auth-field">
                <span>{{ t('settings.currentPassword') }}</span>
                <input
                  class="input"
                  type="password"
                  autocomplete="current-password"
                  [value]="pwdCurrent()"
                  (input)="pwdCurrent.set($any($event.target).value)"
                />
              </label>
              <label class="auth-field">
                <span>{{ t('settings.newPassword') }}</span>
                <input
                  class="input"
                  type="password"
                  autocomplete="new-password"
                  [value]="pwdNext()"
                  (input)="pwdNext.set($any($event.target).value)"
                />
              </label>
              <label class="auth-field">
                <span>{{ t('settings.confirmNew') }}</span>
                <input
                  class="input"
                  type="password"
                  autocomplete="new-password"
                  [value]="pwdConfirm()"
                  (input)="pwdConfirm.set($any($event.target).value)"
                />
              </label>
              <button type="submit" class="btn btn-solid btn-sm" [disabled]="pwdBusy()">
                @if (pwdBusy()) {
                  <span class="spinner"></span>
                } @else {
                  {{ t('settings.savePassword') }}
                }
              </button>
            </form>
          }

          <div style="margin-top: 14px; border-top: 1px solid var(--line); padding-top: 14px">
            <div class="row-between">
              <div>
                <span class="small">{{ t('settings.passkey') }}</span>
                <p class="faint small" style="margin-top: 2px">
                  {{ t('settings.passkey.hint') }}
                </p>
              </div>
              @if (pkSupported) {
                <button
                  type="button"
                  class="btn btn-outline btn-sm"
                  [disabled]="pkBusy()"
                  (click)="addPasskey()"
                >
                  {{ pkBusy() ? t('settings.passkey.waiting') : t('settings.passkey.add') }}
                </button>
              }
            </div>

            @if (pkBlockedByIP) {
              <p class="faint small" style="margin-top: 8px">
                {{ t('settings.passkey.ipBlocked', { port: port }) }}
              </p>
            } @else if (!pkSupported) {
              <p class="faint small" style="margin-top: 8px">
                {{ t('settings.passkey.unsupported') }}
              </p>
            } @else if (passkeys().length === 0) {
              <p class="faint small" style="margin-top: 8px">{{ t('settings.passkey.none') }}</p>
            } @else {
              @for (pk of passkeys(); track pk.id) {
                <div class="row-between" style="margin-top: 10px">
                  <div style="min-width: 0">
                    <div class="small mono">{{ pk.name }}</div>
                    <p class="faint small">
                      {{ t('settings.passkey.addedAt', { time: date(pk.created_at) })
                      }}{{
                        pk.last_used
                          ? t('settings.passkey.lastUsed', { time: date(pk.last_used) })
                          : ''
                      }}
                    </p>
                  </div>
                  <button type="button" class="btn btn-link btn-sm" (click)="askRemovePasskey(pk)">
                    {{ t('settings.passkey.remove') }}
                  </button>
                </div>
              }
            }

            @if (pkErr()) {
              <div class="notice notice-error" style="margin-top: 10px">{{ pkErr() }}</div>
            }
          </div>

          @if (totpOpen()) {
            <div style="margin-top: 14px; border-top: 1px solid var(--line); padding-top: 14px">
              @if (!user()?.totp_enabled) {
                <p class="faint small">{{ t('settings.totp.scan') }}</p>
                <div class="row" style="margin-top: 10px; gap: 14px; align-items: flex-start">
                  @if (totpQr()) {
                    <img
                      [src]="totpQr()"
                      [attr.alt]="t('settings.totp.qrAlt')"
                      width="132"
                      height="132"
                      style="border-radius: 6px; background: #fff; padding: 4px"
                    />
                  }
                  <div style="min-width: 0">
                    <p class="faint small">{{ t('settings.totp.manual') }}</p>
                    <code class="mono" style="font-size: var(--fs-xs); word-break: break-all">{{
                      totpSecret()
                    }}</code>
                  </div>
                </div>
                @if (totpErr()) {
                  <div class="notice notice-error" style="margin-top: 10px">{{ totpErr() }}</div>
                }
                <form class="row" style="margin-top: 12px; gap: 8px" (submit)="submitTotp($event)">
                  <input
                    class="input mono"
                    style="max-width: 160px"
                    inputmode="numeric"
                    maxlength="6"
                    [attr.placeholder]="t('settings.totp.code')"
                    autocomplete="one-time-code"
                    [value]="totpCode()"
                    (input)="totpCode.set($any($event.target).value)"
                  />
                  <button type="submit" class="btn btn-solid btn-sm" [disabled]="totpBusy()">
                    @if (totpBusy()) {
                      <span class="spinner"></span>
                    } @else {
                      {{ t('settings.totp.confirm') }}
                    }
                  </button>
                </form>
              } @else {
                <p class="faint small">{{ t('settings.totp.offHint') }}</p>
                @if (totpErr()) {
                  <div class="notice notice-error" style="margin-top: 10px">{{ totpErr() }}</div>
                }
                <form class="row" style="margin-top: 10px; gap: 8px" (submit)="disableTotp($event)">
                  <input
                    class="input"
                    type="password"
                    style="max-width: 220px"
                    [attr.placeholder]="t('settings.currentPassword')"
                    [value]="totpPwd()"
                    (input)="totpPwd.set($any($event.target).value)"
                  />
                  <button type="submit" class="btn btn-outline btn-sm" [disabled]="totpBusy()">
                    {{ t('settings.totp.off') }}
                  </button>
                </form>
              }
            </div>
          }
        }
      </div>

      @if (pkTarget()) {
        <vx-confirm
          [title]="t('settings.passkey.removeTitle')"
          [desc]="t('settings.passkey.removeDesc')"
          [confirmText]="t('settings.passkey.remove')"
          [danger]="true"
          (confirmed)="removePasskey()"
          (cancelled)="pkTarget.set(null)"
        />
      }

      @if (capOpen()) {
        <vx-captcha-dialog
          [provider]="capProvider()"
          [siteKey]="tsSiteKey()"
          [endpoint]="capEndpoint()"
          [challenge]="cap()"
          [action]="pendingAction()"
          (solved)="onCaptchaSolved($event)"
          (closed)="capOpen.set(false)"
        />
      }

      @if (auth.loggedIn()) {
        <div class="card" style="margin-top: 12px">
          <div class="section-title">{{ t('settings.sessions') }}</div>
          <vx-device-list [items]="sessions()" (detail)="detailDevice.set($event)" />
        </div>

        @if (detailDevice()) {
          <vx-modal
            [title]="t('settings.device.title')"
            [width]="440"
            (closed)="detailDevice.set(null)"
          >
            <p class="small" style="margin: 0 0 14px">
              {{ t('settings.device.prefix') }}
              <a
                routerLink="/settings"
                (click)="$event.preventDefault(); pwdOpen.set(true); detailDevice.set(null)"
                >{{ t('settings.device.link') }}</a
              >{{ t('settings.device.suffix') }}
            </p>
            <div class="section-title" style="margin-bottom: 8px">
              {{ t('settings.device.details') }}
            </div>
            <div class="detail-grid">
              <span class="detail-ico">⏱</span>
              <span>{{ fullDT(detailDevice()!.created_at) }}</span>
            </div>
            <div class="detail-grid">
              <span class="detail-ico">➤</span>
              <span>{{ geoLabelOf(detailDevice()!) }}</span>
            </div>
            @if (detailDevice()!.geo?.isp) {
              <div class="detail-grid">
                <span class="detail-ico">📡</span>
                <span>{{ detailDevice()!.geo!.isp }}</span>
              </div>
            }
            @if (detailDevice()!.geo?.score) {
              <div class="detail-grid">
                <span class="detail-ico">★</span>
                <span>
                  {{
                    t('settings.device.reputation', {
                      score: detailDevice()!.geo!.score,
                      status: detailDevice()!.geo!.status,
                    })
                  }}
                  @for (tag of riskTagsOf(detailDevice()!.geo!.risk); track tag) {
                    <span class="chip chip-danger">{{ tag }}</span>
                  }
                </span>
              </div>
            }
            <div class="detail-grid">
              <span class="detail-ico">🖥</span>
              <span>{{
                t('settings.device.browserOn', {
                  browser: browserOf(detailDevice()!.ua),
                  os: osOf(detailDevice()!.ua),
                })
              }}</span>
            </div>
            <div class="detail-grid">
              <span class="detail-ico">🌐</span>
              <span class="mono">{{ t('settings.device.ip', { ip: detailDevice()!.ip }) }}</span>
            </div>
          </vx-modal>
        }

        @if (logoutConfirm()) {
          <vx-confirm
            [title]="t('settings.logoutTitle')"
            [desc]="t('settings.logoutDesc')"
            [confirmText]="t('settings.logoutConfirm')"
            [danger]="true"
            (confirmed)="logout()"
            (cancelled)="logoutConfirm.set(false)"
          />
        }
      }

      <div class="card" style="margin-top: 12px; padding: 0">
        @for (f of FILTERS; track f.key) {
          <div class="flag">
            <div class="flag-body">
              <div class="section-title">{{ t(f.labelKey) }}</div>
              <p class="faint small">{{ t(f.hintKey) }}</p>
            </div>
            <button
              type="button"
              class="switch"
              role="switch"
              [attr.aria-checked]="exclude().includes(f.key)"
              [attr.aria-label]="t(f.labelKey)"
              [class.is-on]="exclude().includes(f.key)"
              (click)="toggleExclude(f.key)"
            ></button>
          </div>
        }
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">{{ t('settings.prefs') }}</div>
        <div
          class="row"
          style="justify-content: space-between; align-items: center; margin-top: 10px; gap: 12px"
        >
          <span class="section-title">{{ t('settings.prefs.related') }}</span>
          <button
            type="button"
            class="switch"
            role="switch"
            [attr.aria-checked]="relatedPref()"
            [attr.aria-label]="t('settings.prefs.related')"
            [class.is-on]="relatedPref()"
            (click)="toggleRelated()"
          ></button>
        </div>
        <div
          class="row"
          style="justify-content: space-between; align-items: center; margin-top: 10px; gap: 12px"
        >
          <span class="section-title">{{ t('settings.prefs.overview') }}</span>
          <button
            type="button"
            class="switch"
            role="switch"
            [attr.aria-checked]="overviewPref()"
            [attr.aria-label]="t('settings.prefs.overview')"
            [class.is-on]="overviewPref()"
            (click)="toggleOverview()"
          ></button>
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">{{ t('settings.prefs.time') }}</div>
        <div class="row wrap" style="margin-top: 10px">
          @for (w of TIME_WINDOWS; track w.since) {
            <button
              type="button"
              class="chip"
              [class.is-active]="since() === w.since"
              (click)="since.set(w.since)"
            >
              {{ t(w.labelKey) }}
            </button>
          }
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="section-title">{{ t('settings.prefs.theme') }}</div>
        <div class="row wrap" style="margin-top: 10px">
          @for (th of THEMES; track th.value) {
            <button
              type="button"
              class="chip"
              [class.is-active]="theme() === th.value"
              (click)="setTheme(th.value)"
            >
              {{ t(th.labelKey) }}
            </button>
          }
        </div>
      </div>

      <div class="card" style="margin-top: 12px">
        <div class="row-between">
          <div style="min-width: 0">
            <div class="section-title">{{ t('settings.getRequest') }}</div>
            <p class="faint small" style="margin-top: 2px">
              {{ t('settings.getRequest.hint') }}
            </p>
          </div>
          <button
            type="button"
            class="switch"
            role="switch"
            [attr.aria-checked]="getRequest()"
            [attr.aria-label]="t('settings.getRequest')"
            [class.is-on]="getRequest()"
            (click)="toggleGetRequest()"
          ></button>
        </div>
      </div>

      <div class="row" style="margin-top: 16px">
        <button type="button" class="btn btn-solid" (click)="save()">
          <vx-icon name="check" [size]="16" />
          {{ t('settings.save') }}
        </button>
        @if (saved()) {
          <span class="small" style="color: var(--success)" role="status">{{
            t('settings.saved')
          }}</span>
        }
      </div>
    </div>
  `,
})
export class SettingsPage {
  protected readonly t = t;
  private readonly prefsService = inject(PrefsService);
  protected readonly auth = inject(AuthService);
  protected readonly user = this.auth.user;

  protected readonly FILTERS = FILTERS;
  protected readonly TIME_WINDOWS = TIME_WINDOWS;

  protected readonly THEMES: { value: Theme; labelKey: string }[] = [
    { value: 'system', labelKey: 'settings.theme.system' },
    { value: 'light', labelKey: 'settings.theme.light' },
    { value: 'dark', labelKey: 'settings.theme.dark' },
  ];

  protected readonly exclude = signal<string[]>([]);
  protected readonly since = signal(0);
  protected readonly theme = signal<Theme>('system');
  protected readonly saved = signal(false);
  /** 请求方式即时生效：它是行为开关，不像过滤条件那样需要「保存」才应用。 */
  protected readonly getRequest = computed(() => this.prefsService.prefs().getRequest !== false);

  constructor() {
    this.prefsService.load();
    const p = this.prefsService.prefs();
    this.exclude.set([...p.exclude]);
    this.since.set(p.since);
    this.theme.set(this.prefsService.theme());
    void this.auth.whenReady().then(() => {
      void this.loadPasskeys();
      void this.loadSessions();
      void this.loadCaptcha();
    });
  }

  // ---- 账户与安全 ----
  protected readonly emailOpen = signal(false);
  protected readonly emailNew = signal('');
  protected readonly emailPwd = signal('');
  protected readonly emailBusy = signal(false);
  protected readonly emailMsg = signal('');
  protected readonly emailErr = signal('');

  protected readonly pwdOpen = signal(false);
  protected readonly pwdCurrent = signal('');
  protected readonly pwdNext = signal('');
  protected readonly pwdConfirm = signal('');
  protected readonly pwdBusy = signal(false);
  protected readonly pwdMsg = signal('');
  protected readonly pwdErr = signal('');

  protected readonly totpOpen = signal(false);
  protected readonly totpSecret = signal('');
  protected readonly totpQr = signal('');
  protected readonly totpCode = signal('');
  protected readonly totpPwd = signal('');
  protected readonly totpBusy = signal(false);
  protected readonly totpErr = signal('');

  // ---- 通行密钥 ----
  protected readonly pkTarget = signal<{ id: number } | null>(null);
  protected readonly passkeys = signal<
    { id: number; credential_id: string; name: string; created_at: number; last_used: number }[]
  >([]);
  protected readonly pkBusy = signal(false);
  protected readonly pkErr = signal('');
  protected readonly pkSupported = passkeySupported();
  protected readonly pkBlockedByIP = passkeyBlockedByIP();
  protected readonly sessions = signal<DeviceRow[]>([]);
  protected readonly detailDevice = signal<DeviceRow | null>(null);
  /** 是否显示「深入了解 X」相关搜索（本地偏好，默认开）。 */
  protected readonly relatedPref = signal(localStorage.getItem('stdseek.pref.related') !== '0');
  /** 是否显示 AI 概览（本地偏好，默认开；后台功能开关关闭时仍不显示）。 */
  protected readonly overviewPref = this.prefsService.overview;

  // ---- 改密码 / 改邮箱的验证闸门（两者共用一个弹窗，靠 pendingAction 区分） ----
  protected readonly capOpen = signal(false);
  protected readonly capEnabled = signal(false);
  protected readonly capProvider = signal('');
  protected readonly capEndpoint = signal('');
  protected readonly tsSiteKey = signal('');
  protected readonly cap = signal<CaptchaChallenge | null>(null);
  protected readonly capAnswer = signal('');
  protected readonly tsToken = signal('');
  protected readonly pendingAction = signal<'password' | 'email'>('password');

  private async loadCaptcha(): Promise<void> {
    try {
      const r = await this.auth.captcha();
      this.capEnabled.set(!!r.enabled);
      if (!r.enabled) return;
      if (r.provider && r.provider !== 'math') {
        this.capProvider.set(r.provider);
        this.tsSiteKey.set(r.site_key ?? '');
        this.capEndpoint.set((r as { endpoint?: string }).endpoint ?? '');
      } else {
        this.capProvider.set('math');
        this.cap.set(r.challenge ?? null);
      }
    } catch {
      /* 忽略：拿不到就当作不需要验证 */
    }
  }

  private capPassed(): boolean {
    if (this.capProvider() === '' || this.capProvider() === 'math') return this.capAnswer() !== '';
    return this.tsToken() !== '';
  }

  private async openCaptcha(action: 'password' | 'email'): Promise<void> {
    this.pendingAction.set(action);
    if (this.capProvider() === '' || this.capProvider() === 'math') {
      try {
        const r = await this.auth.captcha();
        this.cap.set(r.challenge ?? null);
      } catch {
        /* 忽略 */
      }
      this.capAnswer.set('');
    } else {
      this.tsToken.set('');
    }
    this.capOpen.set(true);
  }

  protected onCaptchaSolved(r: { token?: string; answer?: string }): void {
    if (r.token) this.tsToken.set(r.token);
    if (r.answer) this.capAnswer.set(r.answer);
    this.capOpen.set(false);
    if (this.pendingAction() === 'email') void this.doSubmitEmail();
    else void this.doSubmitPwd();
  }
  protected readonly port = location.port || '8080';

  private async loadSessions(): Promise<void> {
    if (!this.auth.loggedIn()) return;
    try {
      this.sessions.set(await this.auth.sessions());
    } catch {
      /* 读不到就不显示，不打扰 */
    }
  }

  private async loadPasskeys(): Promise<void> {
    if (!this.auth.loggedIn()) return;
    try {
      this.passkeys.set(await this.auth.listPasskeys());
    } catch {
      /* 读不到就当没有，不打扰用户 */
    }
  }

  protected async addPasskey(): Promise<void> {
    this.pkErr.set('');
    if (!this.pkSupported) {
      this.pkErr.set('当前环境不支持通行密钥（需要 HTTPS 或 localhost）');
      return;
    }
    this.pkBusy.set(true);
    try {
      const { session, publicKey } = await this.auth.passkeyRegisterBegin();
      const cred = (await navigator.credentials.create({
        publicKey: toCreationOptions(publicKey),
      })) as PublicKeyCredential | null;
      if (!cred) {
        this.pkErr.set('没有创建通行密钥');
        return;
      }
      await this.auth.passkeyRegisterFinish(session, serializeAttestation(cred));
      await this.loadPasskeys();
    } catch (e) {
      this.pkErr.set(passkeyErrorText(e));
    } finally {
      this.pkBusy.set(false);
    }
  }

  /** 删除通行密钥前确认：删错了下次就得重新登记，且可能暂时只剩密码可登。 */
  protected askRemovePasskey(pk: { id: number }): void {
    this.pkTarget.set(pk);
  }

  protected async removePasskey(): Promise<void> {
    const pk = this.pkTarget();
    this.pkTarget.set(null);
    if (!pk) return;
    this.pkErr.set('');
    try {
      await this.auth.deletePasskey(pk.id);
      await this.loadPasskeys();
    } catch (e) {
      this.pkErr.set(e instanceof Error ? e.message : '删除失败');
    }
  }

  protected date(ts: number): string {
    if (!ts) return '—';
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`;
  }

  protected readonly logoutConfirm = signal(false);
  protected askLogout(): void {
    this.logoutConfirm.set(true);
  }

  protected logout(): void {
    this.logoutConfirm.set(false);
    void this.auth.logout();
  }

  protected toggleRelated(): void {
    const next = !this.relatedPref();
    this.relatedPref.set(next);
    localStorage.setItem('stdseek.pref.related', next ? '1' : '0');
  }

  protected toggleOverview(): void {
    this.prefsService.setOverview(!this.overviewPref());
  }

  protected fullDT(ts: number): string {
    const d = new Date(ts * 1000);
    const p = (n: number) => String(n).padStart(2, '0');
    return `${d.getFullYear()}年${d.getMonth() + 1}月${d.getDate()}日 ${p(d.getHours())}:${p(d.getMinutes())}`;
  }

  /** 从 UA 里取浏览器名（复用 core/ua 的解析，但去掉「电脑」后缀拼接）。 */
  /** 归属地文案：本机回环显示「本机」，没解析过的显示「未知」。 */
  protected geoLabelOf(d: DeviceRow): string {
    if (d.ip === '::1' || d.ip === '127.0.0.1') return '本机';
    return d.geo?.label || '未知';
  }

  protected riskTagsOf(risk: string): string[] {
    try {
      const arr = JSON.parse(risk || '[]');
      return Array.isArray(arr) ? arr : [];
    } catch {
      return [];
    }
  }

  protected browserOf(ua: string): string {
    return t(parseUA(ua).browserKey);
  }

  protected osOf(ua: string): string {
    return t(parseUA(ua).deviceKey);
  }

  protected toggleEmail(): void {
    this.emailOpen.update((v) => !v);
    this.emailMsg.set('');
    this.emailErr.set('');
  }

  protected async submitEmail(e: Event): Promise<void> {
    e.preventDefault();
    this.emailMsg.set('');
    this.emailErr.set('');
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha('email');
      return;
    }
    await this.doSubmitEmail();
  }

  private async doSubmitEmail(): Promise<void> {
    this.emailBusy.set(true);
    try {
      const msg = await this.auth.changeEmail(this.emailPwd(), this.emailNew(), {
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstileToken: this.tsToken(),
      });
      this.emailOpen.set(false);
      this.emailPwd.set('');
      this.emailNew.set('');
      this.emailMsg.set(msg);
    } catch (err) {
      this.emailErr.set(authErrMsg(err));
      this.tsToken.set('');
      this.capAnswer.set('');
    } finally {
      this.emailBusy.set(false);
    }
  }

  protected togglePwd(): void {
    this.pwdOpen.update((v) => !v);
    this.pwdMsg.set('');
    this.pwdErr.set('');
  }

  protected async submitPwd(e: Event): Promise<void> {
    e.preventDefault();
    this.pwdMsg.set('');
    this.pwdErr.set('');
    if (this.pwdNext() !== this.pwdConfirm()) {
      this.pwdErr.set('两次输入的新密码不一致');
      return;
    }
    if (this.capEnabled() && !this.capPassed()) {
      await this.openCaptcha('password');
      return;
    }
    await this.doSubmitPwd();
  }

  private async doSubmitPwd(): Promise<void> {
    this.pwdBusy.set(true);
    try {
      await this.auth.changePassword(this.pwdCurrent(), this.pwdNext(), {
        captcha: this.cap(),
        answer: this.capAnswer(),
        turnstileToken: this.tsToken(),
      });
      // 后端会清空所有会话，所以这里必然是「已登出」状态
      this.pwdOpen.set(false);
      this.pwdCurrent.set('');
      this.pwdNext.set('');
      this.pwdConfirm.set('');
      this.pwdMsg.set('密码已更新，请用新密码重新登录');
    } catch (err) {
      this.pwdErr.set(authErrMsg(err));
      this.tsToken.set('');
      this.capAnswer.set('');
    } finally {
      this.pwdBusy.set(false);
    }
  }

  protected async toggleTotp(): Promise<void> {
    this.totpErr.set('');
    // 已开启时这里只是展开「关闭」表单；未开启时才去要密钥
    if (this.user()?.totp_enabled) {
      this.totpOpen.update((v) => !v);
      return;
    }
    if (this.totpOpen()) {
      this.totpOpen.set(false);
      return;
    }
    this.totpOpen.set(true);
    try {
      const r = await this.auth.totpSetup();
      this.totpSecret.set(r.secret);
      this.totpQr.set(r.qr);
    } catch (err) {
      this.totpErr.set(authErrMsg(err));
    }
  }

  protected async submitTotp(e: Event): Promise<void> {
    e.preventDefault();
    this.totpErr.set('');
    this.totpBusy.set(true);
    try {
      await this.auth.totpEnable(this.totpSecret(), this.totpCode().trim());
      this.totpOpen.set(false);
      this.totpCode.set('');
    } catch (err) {
      this.totpErr.set(authErrMsg(err));
    } finally {
      this.totpBusy.set(false);
    }
  }

  protected async disableTotp(e: Event): Promise<void> {
    e.preventDefault();
    this.totpErr.set('');
    this.totpBusy.set(true);
    try {
      await this.auth.totpDisable(this.totpPwd());
      this.totpOpen.set(false);
      this.totpPwd.set('');
    } catch (err) {
      this.totpErr.set(authErrMsg(err));
    } finally {
      this.totpBusy.set(false);
    }
  }

  protected toggleExclude(key: string): void {
    this.exclude.update((list) =>
      list.includes(key) ? list.filter((x) => x !== key) : [...list, key],
    );
    this.saved.set(false);
  }

  protected setTheme(theme: Theme): void {
    this.theme.set(theme);
    this.prefsService.setTheme(theme);
  }

  protected toggleGetRequest(): void {
    this.prefsService.setGetRequest(!this.getRequest());
  }

  protected save(): void {
    // 带上 getRequest：save 是整体替换，漏掉这个字段就把刚才的开关改回去了
    this.prefsService.save({
      exclude: this.exclude(),
      since: this.since(),
      getRequest: this.getRequest(),
    });
    this.saved.set(true);
  }
}
