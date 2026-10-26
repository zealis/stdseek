import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import {
  Alert, Box, Button, Chip, Container, Divider, MenuItem, Paper, Stack, TextField, Typography,
} from '@mui/material';
import { getSubmission, submitSite, type SubmissionStatus } from '../api';

// 用户提交站点：进审核队列，不进白名单。管理员在「管理后台 → 审核队列」逐条处理。
const CATEGORIES = [
  { value: 'official', label: '官方文档' },
  { value: 'opensource', label: '开源项目' },
  { value: 'blog', label: '技术博客' },
  { value: 'platform', label: '技术社区 / 平台' },
  { value: 'media', label: '技术媒体' },
];

const STATUS_COLOR: Record<string, 'default' | 'warning' | 'success' | 'error'> = {
  pending: 'warning',
  approved: 'success',
  rejected: 'error',
};

// 三步进度：提交 → 审核 → 抓取入库。approved 之后还要等下一次抓取，说清楚才不会让人以为没生效。
const STEPS = ['已提交', '人工审核', '抓取入库'];

function stepIndex(status: string) {
  if (status === 'approved') return 2; // 已审核通过，等下一次抓取
  if (status === 'rejected') return 1; // 卡在审核这一步
  return 0; // pending：只完成「已提交」
}

export default function Contribute() {
  const [params, setParams] = useSearchParams();
  const [form, setForm] = useState({ url: '', category: 'blog', reason: '' });
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState('');
  const [ticket, setTicket] = useState('');
  const [copied, setCopied] = useState(false);

  const [query, setQuery] = useState('');
  const [status, setStatus] = useState<SubmissionStatus | null>(null);
  const [statusErr, setStatusErr] = useState('');
  const [statusBusy, setStatusBusy] = useState(false);

  const loadStatus = useCallback(async (t: string) => {
    const code = t.trim();
    if (!code) return;
    setStatusBusy(true);
    setStatusErr('');
    try {
      setStatus(await getSubmission(code));
    } catch (e) {
      setStatus(null);
      setStatusErr((e as Error).message);
    } finally {
      setStatusBusy(false);
    }
  }, []);

  // 支持把带查询码的链接直接发给用户（?ticket=vx...），刷新后仍能看到进度。
  useEffect(() => {
    const t = params.get('ticket');
    if (t) {
      setQuery(t);
      void loadStatus(t);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const submit = async () => {
    setErr('');
    setBusy(true);
    try {
      const r = await submitSite(form);
      setTicket(r.ticket);
      setQuery(r.ticket);
      setStatus(null);
      setStatusErr('');
      setForm({ url: '', category: 'blog', reason: '' });
      setParams({ ticket: r.ticket }, { replace: true });
      void loadStatus(r.ticket);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const copyTicket = async () => {
    try {
      await navigator.clipboard.writeText(ticket);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      setCopied(false);
    }
  };

  const statusCard = (
    <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper' }}>
      {statusErr && <Alert severity="error" sx={{ mb: 2 }}>{statusErr}</Alert>}
      {status && (
        <>
          <Stack direction="row" spacing={1} alignItems="center" sx={{ flexWrap: 'wrap' }} useFlexGap>
            <Typography sx={{ fontWeight: 600 }}>{status.domain}</Typography>
            <Chip size="small" color={STATUS_COLOR[status.status] ?? 'default'}
              label={status.status_text} sx={{ height: 22 }} />
          </Stack>
          <Stack direction="row" spacing={1} sx={{ mt: 2, mb: 1, alignItems: 'center', flexWrap: 'wrap' }} useFlexGap>
            {STEPS.map((s, i) => {
              const reached = i <= stepIndex(status.status) && status.status !== 'rejected';
              const rejected = status.status === 'rejected' && i === 1;
              return (
                <Stack key={s} direction="row" spacing={0.75} alignItems="center">
                  <Box sx={{
                    width: 8, height: 8, borderRadius: '50%',
                    bgcolor: rejected ? 'error.main' : reached ? 'primary.main' : '#D8D2C4',
                  }} />
                  <Typography variant="caption"
                    color={reached || rejected ? 'text.primary' : 'text.secondary'}>
                    {rejected ? '未通过审核' : s}
                  </Typography>
                  {i < STEPS.length - 1 && <Box sx={{ width: 24, height: 1, bgcolor: '#E0D6C4' }} />}
                </Stack>
              );
            })}
          </Stack>
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block' }}>
            提交于 {new Date(status.created_at * 1000).toLocaleString('zh-CN')}
            {status.status === 'approved' && '（已加入抓取白名单，下一次抓取后即可搜索）'}
          </Typography>
        </>
      )}
      {!status && !statusErr && (
        <Typography variant="body2" color="text.secondary">
          {statusBusy ? '查询中…' : '暂无进度信息'}
        </Typography>
      )}
      <Button size="small" onClick={() => void loadStatus(query)} disabled={statusBusy || !query}
        sx={{ mt: 1.5, minWidth: 0, px: 1.5 }}>
        刷新进度
      </Button>
    </Paper>
  );

  return (
    <Container maxWidth="md" sx={{ pt: 3, pb: 6 }}>
      <Typography variant="h6" sx={{ mb: 0.5 }}>提交站点</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        只收技术内容站点：官方文档、开源项目、技术博客、技术社区、技术媒体。
        提交后进入审核队列，我们会检查可达性、robots.txt 与内容类型；同一域名重复提交会合并。
      </Typography>

      {ticket ? (
        <>
          <Alert severity="success" sx={{ mb: 2 }}>
            已进入审核队列。请保存下方查询码，之后可随时查看进度——它不会再次显示。
          </Alert>
          <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper', mb: 2 }}>
            <Typography variant="caption" color="text.secondary">查询码</Typography>
            <Stack direction="row" spacing={1.5} alignItems="center" sx={{ mt: 0.5, flexWrap: 'wrap' }} useFlexGap>
              <Typography sx={{
                fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace',
                fontSize: '1.05rem', letterSpacing: '0.06em', color: 'primary.main', fontWeight: 600,
              }}>
                {ticket}
              </Typography>
              <Button size="small" variant="outlined" onClick={() => void copyTicket()} sx={{ minWidth: 0, px: 1.5 }}>
                {copied ? '已复制' : '复制'}
              </Button>
            </Stack>
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
              这个查询码只对应你这一次提交，凭它可以查看审核状态，不需要注册账号。
            </Typography>
          </Paper>
          {statusCard}
          <Button sx={{ mt: 2, minWidth: 0, px: 1.5 }} onClick={() => {
            setTicket('');
            setStatus(null);
            setStatusErr('');
            setParams({}, { replace: true });
          }}>
            再提交一个
          </Button>
        </>
      ) : (
        <>
          <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper' }}>
            <Stack spacing={2}>
              <TextField
                fullWidth
                required
                label="网址"
                placeholder="https://example.com 或 example.com"
                value={form.url}
                onChange={(e) => setForm({ ...form, url: e.target.value.trim() })}
                helperText="填站点首页即可，具体抓取入口由管理员按站点结构配置"
              />
              <TextField
                select
                label="分类"
                value={form.category}
                onChange={(e) => setForm({ ...form, category: e.target.value })}
              >
                {CATEGORIES.map((c) => <MenuItem key={c.value} value={c.value}>{c.label}</MenuItem>)}
              </TextField>
              <TextField
                label="理由（可选）"
                placeholder="为什么这个站点值得收录？例如「Redis 官方命令参考」"
                value={form.reason}
                onChange={(e) => setForm({ ...form, reason: e.target.value })}
                multiline
                minRows={2}
                inputProps={{ maxLength: 300 }}
              />
              {err && <Alert severity="error">{err}</Alert>}
              <Button
                variant="contained"
                onClick={() => void submit()}
                disabled={!form.url || busy}
                sx={{ alignSelf: 'flex-start', px: 3 }}
              >
                {busy ? '提交中…' : '提交'}
              </Button>
            </Stack>
          </Paper>

          <Divider sx={{ my: 3 }} />
          <Typography variant="subtitle2" sx={{ mb: 0.5 }}>查询已有提交</Typography>
          <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mb: 1.5 }}>
            输入提交时拿到的查询码（vx 开头）。没有保存的话无法找回，需要重新提交一次。
          </Typography>
          <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5} alignItems={{ sm: 'flex-start' }}>
            {/* 不用 TextField 的 label：小尺寸下标签会压在边框上 */}
            <TextField
              size="small"
              placeholder="vx…"
              value={query}
              onChange={(e) => setQuery(e.target.value.trim())}
              sx={{ flexGrow: 1, maxWidth: { sm: 360 } }}
            />
            <Button variant="outlined" onClick={() => void loadStatus(query)}
              disabled={!query || statusBusy} sx={{ minWidth: 0, px: 2.5, height: 40 }}>
              {statusBusy ? '查询中…' : '查询'}
            </Button>
          </Stack>
          {(status || statusErr) && <Box sx={{ mt: 2 }}>{statusCard}</Box>}
        </>
      )}
    </Container>
  );
}
