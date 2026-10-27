import { HttpClient, HttpErrorResponse } from '@angular/common/http';
import { Injectable, computed, inject, signal } from '@angular/core';
import { lastValueFrom } from 'rxjs';

/** 人机验证题面：a + b，答对即可；带 HMAC 签名，服务端无状态校验。 */
export type CaptchaChallenge = { a: number; b: number; exp: number; sig: string };

/** 登录账号。密码哈希永远不会出现在这个结构里（后端也不返回）。 */
export type AuthUser = {
  id: number;
  email: string;
  role: 'admin' | 'member';
  status: number;
  email_verified: boolean;
  totp_enabled: boolean;
  created_at: number;
  last_login: number;
};

/**
 * 账号状态。会话在 HttpOnly cookie 里，前端拿不到也不需要拿——
 * 唯一的事实来源是 `/api/auth/me`，启动时问一次，之后由登录/登出维护。
 */
@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly http = inject(HttpClient);

  private readonly _user = signal<AuthUser | null>(null);
  /** 首次 me 请求完成前为 false：路由守卫要等它，否则会把已登录的人踢去登录页。 */
  private readonly _ready = signal(false);

  /** 首次 me 完成时兑现。路由守卫要等它，否则刷新页面会把已登录的人踢去登录页。 */
  private readyResolve: (() => void) | null = null;
  private readonly readyPromise = new Promise<void>((resolve) => {
    this.readyResolve = resolve;
  });

  readonly user = this._user.asReadonly();
  readonly ready = this._ready.asReadonly();
  readonly loggedIn = computed(() => this._user() !== null);
  readonly isAdmin = computed(() => this._user()?.role === 'admin');

  constructor() {
    void this.refresh();
  }

  /** 刷新当前账号（启动时、登录后、页面重新聚焦时）。 */
  async refresh(): Promise<void> {
    try {
      const r = await lastValueFrom(this.http.get<{ user: AuthUser | null }>('/api/auth/me'));
      this._user.set(r.user ?? null);
    } catch {
      this._user.set(null);
    } finally {
      this._ready.set(true);
      this.readyResolve?.();
      this.readyResolve = null;
    }
  }

  /** 等首次账号查询结束（守卫用）。 */
  whenReady(): Promise<void> {
    return this.readyPromise;
  }

  /**
   * 问一次服务端要不要人机验证、用哪种方式。
   * provider=math 时带算术题；=turnstile 时带 site key（前端渲染官方组件）。
   */
  async captcha(): Promise<{
    enabled: boolean;
    provider?: 'math' | 'turnstile';
    challenge?: CaptchaChallenge;
    site_key?: string;
  }> {
    return lastValueFrom(
      this.http.get<{
        enabled: boolean;
        provider?: 'math' | 'turnstile';
        challenge?: CaptchaChallenge;
        site_key?: string;
      }>('/api/auth/captcha'),
    );
  }

  async register(
    email: string,
    password: string,
    extra: { captcha?: CaptchaChallenge | null; answer?: string; turnstileToken?: string } = {},
  ): Promise<{ user: AuthUser; needVerify: boolean }> {
    const r = await lastValueFrom(
      this.http.post<{ user: AuthUser; need_verify?: boolean }>('/api/auth/register', {
        email,
        password,
        captcha: extra.captcha ?? null,
        answer: extra.answer ?? '',
        turnstile_token: extra.turnstileToken ?? '',
      }),
    );
    // 要求邮箱验证时服务端不给会话，这里也不能把用户当成已登录
    if (!r.need_verify) this._user.set(r.user);
    return { user: r.user, needVerify: !!r.need_verify };
  }

  async login(
    email: string,
    password: string,
    totp = '',
    extra: { captcha?: CaptchaChallenge | null; answer?: string; turnstileToken?: string } = {},
  ): Promise<AuthUser> {
    const r = await lastValueFrom(
      this.http.post<{ user: AuthUser }>('/api/auth/login', {
        email,
        password,
        totp,
        captcha: extra.captcha ?? null,
        answer: extra.answer ?? '',
        turnstile_token: extra.turnstileToken ?? '',
      }),
    );
    this._user.set(r.user);
    return r.user;
  }

  async logout(): Promise<void> {
    try {
      await lastValueFrom(this.http.post('/api/auth/logout', {}));
    } finally {
      this._user.set(null);
    }
  }

  async forgot(
    email: string,
    extra: { captcha?: CaptchaChallenge | null; answer?: string; turnstileToken?: string } = {},
  ): Promise<string> {
    const r = await lastValueFrom(
      this.http.post<{ message: string }>('/api/auth/forgot', {
        email,
        captcha: extra.captcha ?? null,
        answer: extra.answer ?? '',
        turnstile_token: extra.turnstileToken ?? '',
      }),
    );
    return r.message;
  }

  async reset(token: string, password: string): Promise<string> {
    const r = await lastValueFrom(
      this.http.post<{ message: string }>('/api/auth/reset', { token, password }),
    );
    return r.message;
  }

  async changePassword(current: string, next: string): Promise<void> {
    await lastValueFrom(this.http.post('/api/auth/password', { current, next }));
    // 改密码会清空全部会话（包括当前这条），本地状态必须同步清掉
    this._user.set(null);
  }

  // ---- 通行密钥 ----

  /** 开始注册：拿浏览器的创建选项。 */
  async passkeyRegisterBegin(): Promise<{ session: string; publicKey: any }> {
    return lastValueFrom(
      this.http.post<{ session: string; publicKey: any }>('/api/auth/passkey/register/begin', {}),
    );
  }

  async passkeyRegisterFinish(session: string, credential: unknown): Promise<void> {
    await lastValueFrom(
      this.http.post('/api/auth/passkey/register/finish', { session, credential }),
    );
  }

  async passkeyLoginBegin(email: string): Promise<{ session: string; publicKey: any }> {
    return lastValueFrom(
      this.http.post<{ session: string; publicKey: any }>('/api/auth/passkey/login/begin', { email }),
    );
  }

  async passkeyLoginFinish(session: string, credential: unknown): Promise<AuthUser> {
    const r = await lastValueFrom(
      this.http.post<{ user: AuthUser }>('/api/auth/passkey/login/finish', { session, credential }),
    );
    this._user.set(r.user);
    return r.user;
  }

  async listPasskeys(): Promise<{ id: number; credential_id: string; name: string; created_at: number; last_used: number }[]> {
    const r = await lastValueFrom(
      this.http.get<{ items: { id: number; credential_id: string; name: string; created_at: number; last_used: number }[] }>(
        '/api/auth/passkeys',
      ),
    );
    return r.items ?? [];
  }

  async deletePasskey(id: number): Promise<void> {
    await lastValueFrom(this.http.post('/api/auth/passkey/delete', { id }));
  }

  /** 改邮箱：需要当前密码；成功后所有会话失效（邮箱是登录标识）。 */
  async changeEmail(password: string, email: string): Promise<string> {
    const r = await lastValueFrom(
      this.http.post<{ message: string }>('/api/auth/email', { password, email }),
    );
    this._user.set(null);
    return r.message;
  }

  /** 两步验证：拿密钥（还没生效，等用户输一次验证码确认）。 */
  /** qr 是服务端画好的 PNG data URI（不在浏览器里生成二维码，省一个依赖）。 */
  async totpSetup(): Promise<{ secret: string; otpauth: string; qr: string }> {
    return lastValueFrom(
      this.http.post<{ secret: string; otpauth: string; qr: string }>('/api/auth/totp/setup', {}),
    );
  }

  async totpEnable(secret: string, code: string): Promise<void> {
    await lastValueFrom(this.http.post('/api/auth/totp/enable', { secret, code }));
    await this.refresh();
  }

  async totpDisable(password: string): Promise<void> {
    await lastValueFrom(this.http.post('/api/auth/totp/disable', { password }));
    await this.refresh();
  }
}

/** 把 HttpErrorResponse 里的后端文案取出来，取不到就给状态码。 */
export function authErrMsg(e: unknown): string {
  if (e instanceof HttpErrorResponse) {
    const body = e.error as { error?: string } | null;
    if (body?.error) return body.error;
    if (e.status === 429) return '请求过于频繁，请稍后再试';
    if (e.status === 0) return '无法连接服务器';
    return `请求失败（${e.status}）`;
  }
  return '发生未知错误';
}
