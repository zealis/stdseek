import { Injectable, signal } from '@angular/core';

const TOKEN_KEY = 'voidxo.adminToken';

/**
 * 管理员令牌：纯客户端状态，只存本机 localStorage。
 * 令牌在服务端启动日志中打印（-admin-token 可固定），由 HTTP 拦截器附加到 /admin 请求。
 */
@Injectable({ providedIn: 'root' })
export class AdminTokenService {
  private readonly _token = signal<string>('');

  /** 组件可读的响应式令牌（SSR 无此问题：本应用是纯 SPA）。 */
  readonly token = this._token.asReadonly();

  constructor() {
    try {
      this._token.set(localStorage.getItem(TOKEN_KEY) ?? '');
    } catch {
      this._token.set('');
    }
  }

  set(token: string): void {
    try {
      if (token) localStorage.setItem(TOKEN_KEY, token);
      else localStorage.removeItem(TOKEN_KEY);
    } catch {
      /* 无痕模式等场景下 localStorage 不可用：令牌只活在当前会话 */
    }
    this._token.set(token);
  }

  clear(): void {
    this.set('');
  }
}
