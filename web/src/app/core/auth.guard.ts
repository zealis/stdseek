import { inject } from '@angular/core';
import { type CanActivateFn, Router } from '@angular/router';
import { AuthService } from './auth.service';

/**
 * 后台路由守卫：必须是已登录的管理员。
 *
 * 必须 await whenReady()——页面刷新时 /api/auth/me 还在路上，直接读 isAdmin()
 * 会拿到 false，把刚登录的管理员又踢回登录页。
 */
export const adminGuard: CanActivateFn = async (_route, state) => {
  const auth = inject(AuthService);
  const router = inject(Router);
  await auth.whenReady();
  if (auth.isAdmin()) return true;
  return router.createUrlTree(['/login'], { queryParams: { redirect: state.url } });
};

/** 普通登录守卫：改账号设置等页面用。 */
export const authGuard: CanActivateFn = async (_route, state) => {
  const auth = inject(AuthService);
  const router = inject(Router);
  await auth.whenReady();
  if (auth.loggedIn()) return true;
  return router.createUrlTree(['/login'], { queryParams: { redirect: state.url } });
};
