import { useState } from 'react';
import {
  Alert, Box, Button, Card, CardContent, Chip, CircularProgress, Dialog, DialogActions,
  DialogContent, DialogTitle, Divider, Link, Stack, Tooltip, Typography,
} from '@mui/material';
import { pageSnapshot, summarize, type SummaryResp } from '../api';
import LabelFeedbackDialog, { labelText } from './LabelFeedbackDialog';

export type HitView = {
  url: string;
  domain: string;
  title: string;
  description: string;
  labels: string[] | null;
  label_reasons?: Record<string, string> | null;
  score: number;
  reasons: string[] | null;
  time_by_fetch: boolean;
  sort_time: number;
};

function fmtTime(ts: number, byFetch: boolean) {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  const s = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;
  return byFetch ? `${s}（按收录时间）` : s;
}

// 在原文快照里把引用片段包成 <mark>。
// 只做一次精确子串替换，替换不到就只打开快照不高亮——
// 不为了"一定要高亮"去做模糊匹配，那会把高亮打到错误的段落上。
//
// 定位靠 id + URL fragment（见 buildSnapshotSrc）：快照动辄两万像素高，
// 点开停在第 0 屏等于没定位。试过 autofocus + tabindex（HTML 全局属性，
// 理论上会聚焦并滚动），但在 sandbox="" 的 iframe 里被禁，activeElement 仍是 BODY。
const CITE_ANCHOR = 'voidxo-cite';

function highlightQuote(html: string, quote: string): { html: string; hit: boolean } {
  if (!quote) return { html, hit: false };
  const esc = quote.replace(/[&<>"]/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c] as string));
  const idx = html.indexOf(esc);
  if (idx < 0) return { html, hit: false };
  const mark = `<mark id="${CITE_ANCHOR}" style="background:#FFE58F;color:#1A1A1A;padding:2px;border-radius:2px;scroll-margin-top:24px">${esc}</mark>`;
  return { html: html.slice(0, idx) + mark + html.slice(idx + esc.length), hit: true };
}

// buildSnapshotSrc 决定快照怎么喂给 iframe。
//
// 有命中片段时用 data: URL + #fragment：fragment 能让浏览器直接滚到高亮处，
// 而 srcdoc 不支持 fragment 跳转。data: URL 不是没有代价——超过约 2MB 会被浏览器拒绝，
// 所以留了长度上限，超了退回 srcdoc（内容完整、只是不自动定位）。
const DATA_URL_LIMIT = 1_200_000;

function buildSnapshotSrc(html: string, hit: boolean): string {
  if (!hit) return '';
  const url = 'data:text/html;charset=utf-8,' + encodeURIComponent(html);
  if (url.length > DATA_URL_LIMIT) return '';
  return url + '#' + CITE_ANCHOR;
}

// 证据链：抓取时存进 KV 的原文快照。接口返回的是不可信 HTML，
// 只能用 sandbox iframe 渲染——sandbox="" 会禁掉脚本/表单/同源访问，
// 页面里的 JS 无法碰到宿主页面。相对资源靠注入 <base> 还原，否则图片和样式全丢。
export default function ResultCard({ hit, q, aiSummary }: {
  hit: HitView;
  // q 用于给摘要定位原文；没有查询词时不显示摘要入口
  q?: string;
  // aiSummary 由 /api/features 决定，后台关闭时整块 UI 都不出现
  aiSummary?: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [html, setHtml] = useState('');
  const [frameSrc, setFrameSrc] = useState('');
  const [err, setErr] = useState('');
  const [fbOpen, setFbOpen] = useState(false);

  const [sumOpen, setSumOpen] = useState(false);
  const [sumLoading, setSumLoading] = useState(false);
  const [sum, setSum] = useState<SummaryResp | null>(null);
  const [sumErr, setSumErr] = useState('');

  const showSnapshot = async (quote = '') => {
    setOpen(true);
    setLoading(true);
    setHtml('');
    setFrameSrc('');
    setErr('');
    try {
      const r = await pageSnapshot(hit.url);
      const withBase = r.html.replace(/<head([^>]*)>/i, `<head$1><base href="${hit.url}">`);
      const { html: marked, hit: found } = highlightQuote(withBase, quote);
      setHtml(marked);
      setFrameSrc(buildSnapshotSrc(marked, found));
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  const loadSummary = async () => {
    if (!q) return;
    if (sum) { setSumOpen((v) => !v); return; } // 已经生成过就只切换折叠，不重复消耗
    setSumOpen(true);
    setSumLoading(true);
    setSumErr('');
    try {
      setSum(await summarize(hit.url, q));
    } catch (e) {
      setSumErr((e as Error).message);
    } finally {
      setSumLoading(false);
    }
  };

  return (
    <>
      <Card variant="outlined" sx={{ bgcolor: 'background.paper', '&:hover': { borderColor: 'primary.main' } }}>
        <CardContent sx={{ pb: '12px !important' }}>
          <Link href={hit.url} target="_blank" rel="noreferrer" underline="hover"
            sx={{ color: 'primary.main', fontWeight: 600, fontSize: '1.05rem' }}>
            {hit.title || hit.url}
          </Link>
          <Stack direction="row" spacing={1} alignItems="center" sx={{ mt: 0.5, mb: 1, flexWrap: 'wrap' }} useFlexGap>
            <Typography variant="caption" color="text.secondary">{hit.domain}</Typography>
            {hit.sort_time > 0 && (
              <Typography variant="caption" color="text.secondary">· {fmtTime(hit.sort_time, hit.time_by_fetch)}</Typography>
            )}
            {hit.labels?.map((l) => (
              <Tooltip key={l} title={hit.label_reasons?.[l] || '按站点特征自动归类'} arrow>
                <Chip label={labelText(l)} size="small" variant="outlined"
                  sx={{ borderColor: '#E0D6C4', color: 'secondary.main', height: 20 }} />
              </Tooltip>
            ))}
            {(hit.labels?.length ?? 0) > 0 && (
              <Button size="small" onClick={() => setFbOpen(true)}
                sx={{ minWidth: 0, px: 0.75, py: 0, fontSize: '0.72rem', color: 'text.secondary' }}>
                标签有误？
              </Button>
            )}
          </Stack>
          <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
            {hit.description || '（无摘要）'}
          </Typography>
          <Stack direction="row" spacing={0.5} sx={{ flexWrap: 'wrap' }} useFlexGap alignItems="center">
            {(hit.reasons ?? []).map((r) => (
              <Chip key={r} label={r} size="small"
                sx={{ bgcolor: 'rgba(47,79,79,0.07)', color: 'primary.main', height: 22 }} />
            ))}
            <Box sx={{ flexGrow: 1 }} />
            {aiSummary && q && (
              <Button size="small" onClick={() => void loadSummary()} disabled={sumLoading}
                sx={{ minWidth: 0, px: 1, fontSize: '0.78rem', color: 'primary.main' }}>
                {sumLoading ? '生成摘要…' : sumOpen ? '收起摘要' : 'AI 摘要'}
              </Button>
            )}
            <Button size="small" onClick={() => void showSnapshot()}
              sx={{ minWidth: 0, px: 1, fontSize: '0.78rem', color: 'text.secondary' }}>
              查看快照
            </Button>
          </Stack>

          {sumOpen && (
            <Box sx={{ mt: 1.5, pt: 1.5, borderTop: '1px dashed #E4E4DA' }}>
              {sumLoading && (
                <Stack direction="row" spacing={1} alignItems="center">
                  <CircularProgress size={14} />
                  <Typography variant="caption" color="text.secondary">正在从正文里摘录证据…</Typography>
                </Stack>
              )}
              {sumErr && <Alert severity="warning" sx={{ py: 0.25 }}>{sumErr}</Alert>}
              {sum && sum.empty && (
                <Box>
                  <Typography variant="body2" sx={{ color: 'secondary.main', fontWeight: 600 }}>
                    未找到可靠来源，不生成摘要
                  </Typography>
                  {sum.reason && (
                    <Typography variant="caption" color="text.secondary">{sum.reason}</Typography>
                  )}
                </Box>
              )}
              {sum && !sum.empty && (
                <Box>
                  <Stack spacing={1}>
                    {(sum.conclusions ?? []).map((c, i) => (
                      <Box key={i} component="div">
                        <Typography variant="body2" component="span" sx={{ lineHeight: 1.75 }}>
                          {c.text}
                        </Typography>
                        {(c.citations ?? []).map((ci) => (
                          <Typography
                            key={ci.id}
                            component="span"
                            onClick={() => void showSnapshot(ci.text)}
                            title="点击在原文快照中定位这段"
                            sx={{
                              ml: 0.25, color: 'primary.main', fontSize: '0.72rem',
                              cursor: 'pointer', verticalAlign: 'super', textDecoration: 'underline dotted',
                            }}
                          >
                            [{ci.id}]
                          </Typography>
                        ))}
                        {(c.citations ?? []).length === 0 && (
                          <Chip size="small" label="未验证" variant="outlined"
                            sx={{ ml: 0.75, height: 18, fontSize: '0.68rem', color: 'secondary.main' }} />
                        )}
                      </Box>
                    ))}
                  </Stack>
                  <Divider sx={{ my: 1 }} />
                  <Typography variant="caption" color="text.secondary">
                    {sum.provider === 'extractive'
                      ? '抽取式摘要：直接摘录原文句子，未经模型改写'
                      : `由 ${sum.provider} 生成`}
                    {(sum.conclusions?.length ?? 0) > 0 && ' · 点击 [编号] 在原文中定位'}
                  </Typography>
                  {sum.reason && (
                    <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
                      {sum.reason}
                    </Typography>
                  )}
                </Box>
              )}
            </Box>
          )}
        </CardContent>
      </Card>

      <Dialog open={open} onClose={() => setOpen(false)} maxWidth="lg" fullWidth>
        <DialogTitle sx={{ fontSize: '1rem', pb: 1 }}>
          原文快照
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
            抓取当时保存的页面，用于核对索引内容是否与原文一致
          </Typography>
        </DialogTitle>
        <DialogContent dividers sx={{ p: 0, height: '68vh', bgcolor: '#fff' }}>
          {loading && (
            <Box sx={{ display: 'flex', justifyContent: 'center', alignItems: 'center', height: '100%' }}>
              <CircularProgress size={26} />
            </Box>
          )}
          {!loading && err && <Alert severity="warning" sx={{ m: 2 }}>{err}</Alert>}
          {!loading && (html || frameSrc) && (
            <iframe
              title="原文快照"
              sandbox=""
              {...(frameSrc ? { src: frameSrc } : { srcDoc: html })}
              style={{ width: '100%', height: '100%', border: 0, display: 'block' }}
            />
          )}
        </DialogContent>
        <DialogActions sx={{ px: 2, py: 1 }}>
          <Button size="small" href={hit.url} target="_blank" rel="noreferrer">打开原网页</Button>
          <Button size="small" onClick={() => setOpen(false)}>关闭</Button>
        </DialogActions>
      </Dialog>

      <LabelFeedbackDialog
        open={fbOpen}
        onClose={() => setFbOpen(false)}
        url={hit.url}
        labels={hit.labels ?? []}
        reasons={hit.label_reasons ?? null}
      />
    </>
  );
}
