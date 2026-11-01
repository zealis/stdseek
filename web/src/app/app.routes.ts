import { type Routes } from '@angular/router';
import { adminGuard } from './core/auth.guard';
import { HomePage } from './pages/home';

export const routes: Routes = [
  // 首页是静态导入（不进懒加载）：它是绝大多数人的落地页，而懒加载要多一次往返——
  // 骨架已经把 hero 画出来了，用户这时敲字却没反应，正是「首页感觉慢」的那一段。
  // 代价是 main 里多了 2KB(gzip)，换来打开即可输入。
  { path: '', component: HomePage, title: '搜索' },
  { path: 'search', loadComponent: () => import('./pages/search').then((m) => m.SearchPage), title: '搜索' },
  { path: 'contribute', loadComponent: () => import('./pages/contribute').then((m) => m.ContributePage), title: '提交站点' },
  { path: 'settings', loadComponent: () => import('./pages/settings').then((m) => m.SettingsPage), title: '设置' },
  // 用 api-docs 而不是 /api：/api/* 是后端路由，SPA fallback 抢不到。
  { path: 'api-docs', loadComponent: () => import('./pages/api-docs').then((m) => m.ApiDocsPage), title: 'API' },
  { path: 'usage', loadComponent: () => import('./pages/usage').then((m) => m.UsagePage), title: '每日用量' },
  { path: 'help', loadComponent: () => import('./pages/help').then((m) => m.HelpPage), title: '帮助' },
  { path: 'privacy', loadComponent: () => import('./pages/privacy').then((m) => m.PrivacyPage), title: '隐私' },
  // 账号：这四个页面公开可访问
  { path: 'login', loadComponent: () => import('./pages/auth/login').then((m) => m.LoginPage), title: '登录' },
  { path: 'register', loadComponent: () => import('./pages/auth/register').then((m) => m.RegisterPage), title: '注册' },
  { path: 'forgot', loadComponent: () => import('./pages/auth/forgot').then((m) => m.ForgotPage), title: '找回密码' },
  { path: 'reset', loadComponent: () => import('./pages/auth/reset').then((m) => m.ResetPage), title: '设置新密码' },
  {
    path: 'manage',
    canActivate: [adminGuard],
    loadChildren: () => import('./pages/admin/routes').then((m) => m.ADMIN_ROUTES),
  },
  { path: '**', redirectTo: '' },
];
