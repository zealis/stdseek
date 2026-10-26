import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { AdminTokenService } from './token.service';

/** 后台请求统一带 X-Admin-Token（服务端 -admin-token 打印在启动日志）。 */
export const adminTokenInterceptor: HttpInterceptorFn = (req, next) => {
  const token = inject(AdminTokenService).token();
  if (!token || !req.url.startsWith('/admin/')) return next(req);
  return next(req.clone({ setHeaders: { 'X-Admin-Token': token } }));
};

/** 把后端错误（{error: "..."}）与网络错误统一成一条可展示的中文消息。 */
export function errMsg(err: unknown): string {
  if (err instanceof HttpErrorResponse) {
    const data = err.error as { error?: string } | null;
    return data?.error || err.message || err.statusText || '请求失败';
  }
  if (err instanceof Error) return err.message;
  return '请求失败';
}
