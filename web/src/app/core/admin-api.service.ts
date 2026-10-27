import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { lastValueFrom } from 'rxjs';
import type { AuthUser } from './auth.service';
import type {
  BlacklistSite,
  DimRow,
  FeatureFlag,
  LabelFeedback,
  PrecheckResult,
  ReviewItem,
  UsageRow,
  WhitelistSite,
} from './api';

/** 管理接口（/admin/*）。令牌由 adminTokenInterceptor 统一附加。 */
@Injectable({ providedIn: 'root' })
export class AdminApi {
  private readonly http = inject(HttpClient);

  /** 每日用量（最近 days 天，缺省 30）。只有有数据的天会返回。 */
  usage(days = 30) {
    const params = new HttpParams({ fromObject: { days: String(days) } });
    return lastValueFrom(
      this.http.get<{ days: number; rows: UsageRow[] }>('/admin/usage', { params }),
    );
  }

  /** 人机验证配置。secret 不下发明文，只有 has_secret。 */
  getCaptchaConfig() {
    return lastValueFrom(
      this.http.get<{
        provider: string;
        site_key: string;
        endpoint: string;
        has_secret: boolean;
        ready: boolean;
      }>('/admin/captcha'),
    );
  }

  setCaptchaConfig(body: { provider: string; site_key: string; secret: string; endpoint: string }) {
    return lastValueFrom(
      this.http.post<{ provider: string; ready: boolean }>('/admin/captcha', body),
    );
  }

  /** 账号列表（后台）。kw 非空时按邮箱模糊搜索。 */
  listUsers(page: number, size = 20, kw = '') {
    const params = new HttpParams({ fromObject: { page: String(page), size: String(size), kw } });
    return lastValueFrom(
      this.http.get<{ total: number; items: AuthUser[]; page: number }>('/admin/users', { params }),
    );
  }

  /** 批量改角色或启停。 */
  batchUsers(ids: number[], action: 'role' | 'status', value: string) {
    return lastValueFrom(
      this.http.post<{ status: string }>('/admin/users/batch', { ids, action, value }),
    );
  }

  setUserRole(id: number, role: 'admin' | 'member') {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/users/${id}/role`, { role }));
  }

  setUserStatus(id: number, status: number) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/users/${id}/status`, { status }));
  }

  /** 用量维度排行：API 调用来源域名与请求地区。 */
  usageDims(days = 30) {
    const params = new HttpParams({ fromObject: { days: String(days) } });
    return lastValueFrom(
      this.http.get<{
        days: number;
        origins: DimRow[];
        countries: DimRow[];
        geo_header: string;
      }>('/admin/usage/dims', { params }),
    );
  }

  // ---- 白名单 ----

  listWhitelist(kw: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: WhitelistSite[] }>('/admin/whitelist', { params }),
    );
  }

  addWhitelist(body: { domain: string; category: string; crawl_interval?: number }) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/whitelist', body));
  }

  toggleWhitelist(id: number, enabled: boolean) {
    return lastValueFrom(
      this.http.post<{ status: string }>(`/admin/whitelist/${id}/toggle`, { enabled }),
    );
  }

  // ---- 黑名单 ----

  listBlacklist(kw: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: BlacklistSite[] }>('/admin/blacklist', { params }),
    );
  }

  addBlacklist(body: { domain: string; reason: string }) {
    return lastValueFrom(this.http.post<{ status: string }>('/admin/blacklist', body));
  }

  removeBlacklist(id: number) {
    return lastValueFrom(this.http.delete<{ status: string }>(`/admin/blacklist/${id}`));
  }

  // ---- 审核队列 ----

  listReview(status: string, kw: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { status, kw, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: ReviewItem[] }>('/admin/review', { params }),
    );
  }

  /** 队列里存的预检是历史 JSON；提交时后端已同步预检过一次，这里可随时重跑。 */
  precheckReview(id: number) {
    return lastValueFrom(
      this.http.post<{ id: number; precheck: PrecheckResult; blacklisted: boolean }>(
        `/admin/review/${id}/precheck`,
        {},
      ),
    );
  }

  approveReview(id: number) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/review/${id}/approve`, {}));
  }

  rejectReview(id: number, reason: string) {
    return lastValueFrom(this.http.post<{ status: string }>(`/admin/review/${id}/reject`, { reason }));
  }

  // ---- 预检（管理员直提前手动触发） ----

  precheck(url: string) {
    return lastValueFrom(
      this.http.post<{ precheck: PrecheckResult; blacklisted: boolean }>('/admin/precheck', { url }),
    );
  }

  // ---- 标签反馈 ----

  listLabelFeedback(kw: string, verdict: string, page: number, size = 10) {
    const params = new HttpParams({ fromObject: { kw, verdict, page: String(page), size: String(size) } });
    return lastValueFrom(
      this.http.get<{ total: number; items: LabelFeedback[]; by_label: Record<string, number> | null }>(
        '/admin/label-feedback',
        { params },
      ),
    );
  }

  deleteLabelFeedback(ids: number[]) {
    return lastValueFrom(
      this.http.post<{ deleted: number }>('/admin/label-feedback/delete', { ids }),
    );
  }

  // ---- 功能开关：标题与说明由服务端下发，前端不维护开关清单 ----

  listFeatures() {
    return lastValueFrom(this.http.get<{ features: FeatureFlag[] }>('/admin/settings'));
  }

  setFeature(key: string, enabled: boolean) {
    return lastValueFrom(
      this.http.post<{ key: string; enabled: boolean }>('/admin/settings/feature', { key, enabled }),
    );
  }
}
