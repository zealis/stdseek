import { HttpClient, HttpParams } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { lastValueFrom } from 'rxjs';
import type {
  Hit,
  SearchResp,
  Status,
  SubmissionStatus,
  SummaryResp,
} from './api';

/**
 * 公开接口（/api/*）。搜索、提交、反馈、快照、摘要、运行状态。
 * 全部同源相对路径，单二进制部署时由 Go 直接提供。
 */
@Injectable({ providedIn: 'root' })
export class PublicApi {
  private readonly http = inject(HttpClient);

  search(q: string, opts: { exclude?: string[]; since?: number; page?: number; size?: number } = {}) {
    let params = new HttpParams({ fromObject: { q, page: String(opts.page ?? 1), size: String(opts.size ?? 10) } });
    if (opts.exclude?.length) params = params.set('no', opts.exclude.join(','));
    if (opts.since) params = params.set('since', String(opts.since));
    return lastValueFrom(this.http.get<SearchResp>('/api/search', { params }));
  }

  /** 用户提交站点（公开接口，进审核队列）。返回的 ticket 是查询凭证。 */
  submitSite(body: { url: string; category: string; reason?: string }) {
    return lastValueFrom(
      this.http.post<{ status: string; ticket: string }>('/api/submit', body),
    );
  }

  getSubmission(ticket: string) {
    const params = new HttpParams({ fromObject: { ticket } });
    return lastValueFrom(this.http.get<SubmissionStatus>('/api/submission', { params }));
  }

  /** 标签误判反馈：verdict 为 false_positive（打错了）或 confirmed（打得对）。 */
  submitLabelFeedback(body: { url: string; label: string; verdict: 'false_positive' | 'confirmed'; note?: string }) {
    return lastValueFrom(this.http.post<{ status: string }>('/api/label-feedback', body));
  }

  /** 抓取时保存的原文快照（证据链）。返回的 html 是不可信内容，只能进 sandbox iframe。 */
  pageSnapshot(url: string) {
    const params = new HttpParams({ fromObject: { url } });
    return lastValueFrom(
      this.http.get<{ url: string; key: string; size: number; html: string }>('/api/page', { params }),
    );
  }

  /** 公开功能开关：前台据此决定是否渲染 AI 摘要入口。失败按「全关」处理。 */
  publicFeatures() {
    return lastValueFrom(this.http.get<{ features: Record<string, boolean> }>('/api/features'))
      .catch(() => ({ features: {} as Record<string, boolean> }));
  }

  /** 首页运行状态；失败按空库处理，保证首屏总能渲染。 */
  status() {
    return lastValueFrom(this.http.get<Status>('/api/status'))
      .catch(() => ({ documents: 0, sites: 0, features: {} as Record<string, boolean> }));
  }

  /** 证据链摘要：conclusions 为空或 empty=true 时 UI 显示「未验证 / 未找到可靠来源」。 */
  summarize(url: string, q: string) {
    return lastValueFrom(this.http.post<SummaryResp>('/api/summary', { url, q }));
  }
}

export type { Hit };
