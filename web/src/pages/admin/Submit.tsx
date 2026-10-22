import { useState } from 'react';
import {
  Alert, Button, Chip, MenuItem, Paper, Stack, TextField, Typography,
} from '@mui/material';
import { addWhitelist, precheck, type PrecheckResult } from '../../api';

const CATEGORIES = ['official', 'opensource', 'blog', 'platform', 'media'];

// 管理员直接提交站点进白名单（与用户提交审核队列区分）
export default function Submit() {
  const [form, setForm] = useState({ domain: '', category: 'official', crawl_interval: 86400 });
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');
  const [pre, setPre] = useState<PrecheckResult | null>(null);
  const [blacklisted, setBlacklisted] = useState(false);
  const [checking, setChecking] = useState(false);

  const runPrecheck = async () => {
    setErr('');
    setPre(null);
    setBlacklisted(false);
    setChecking(true);
    try {
      const r = await precheck(form.domain);
      setPre(r.precheck);
      setBlacklisted(r.blacklisted);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setChecking(false);
    }
  };

  const submit = async () => {
    setErr('');
    setMsg('');
    try {
      await addWhitelist(form);
      setMsg(`${form.domain} 已加入白名单，将作为爬虫种子。`);
      setForm({ domain: '', category: 'official', crawl_interval: 86400 });
      setPre(null);
      setBlacklisted(false);
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper', maxWidth: 680 }}>
      <Typography variant="h6" sx={{ mb: 0.5 }}>提交站点</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        管理员提交的站点直接进入白名单。用户侧提交入口当前关闭（首批 20–50 站质量验证前不开放）。
      </Typography>
      <Stack spacing={2}>
        <Stack direction="row" spacing={1}>
          <TextField
            fullWidth
            label="域名"
            placeholder="go.dev"
            value={form.domain}
            onChange={(e) => { setForm({ ...form, domain: e.target.value.trim().toLowerCase() }); setPre(null); }}
          />
          <Button variant="outlined" onClick={() => void runPrecheck()} disabled={!form.domain || checking}
            sx={{ whiteSpace: 'nowrap', flexShrink: 0 }}>
            {checking ? '预检中…' : '预检'}
          </Button>
        </Stack>

        {pre && (
          <Stack spacing={1}>
            <Stack direction="row" spacing={1} useFlexGap sx={{ flexWrap: 'wrap' }}>
              <Chip
                label={pre.reachable ? `可达 ${pre.status_code}` : `不可达${pre.status_code ? ` ${pre.status_code}` : ''}`}
                color={pre.reachable ? 'primary' : 'error'}
                variant={pre.reachable ? 'filled' : 'outlined'}
              />
              <Chip
                label={pre.is_html ? 'HTML 页面' : `非 HTML（${pre.content_type || '未知'}）`}
                color={pre.is_html ? 'primary' : 'default'}
                variant={pre.is_html ? 'filled' : 'outlined'}
              />
              <Chip
                label={pre.robots_found ? (pre.robots_allowed ? 'robots 允许' : 'robots 禁止') : '无 robots.txt'}
                color={pre.robots_allowed ? 'primary' : 'error'}
                variant={pre.robots_allowed ? 'filled' : 'outlined'}
              />
              {blacklisted && <Chip label="命中黑名单历史" color="error" />}
            </Stack>
            {pre.error && <Alert severity="error">{pre.error}</Alert>}
            {!pre.reachable && !pre.error && <Alert severity="warning">站点不可达，加入白名单后爬虫会记录失败状态。</Alert>}
            {!pre.robots_allowed && <Alert severity="warning">robots.txt 禁止抓取首页，请确认后再加入。</Alert>}
            {pre.final_url && pre.final_url !== pre.url && (
              <Typography variant="caption" color="text.secondary">最终地址：{pre.final_url}</Typography>
            )}
          </Stack>
        )}

        <TextField select label="分类" value={form.category}
          onChange={(e) => setForm({ ...form, category: e.target.value })}>
          {CATEGORIES.map((c) => <MenuItem key={c} value={c}>{c}</MenuItem>)}
        </TextField>
        <TextField label="抓取间隔（秒）" type="number" value={form.crawl_interval}
          onChange={(e) => setForm({ ...form, crawl_interval: Number(e.target.value) })} />
        {err && <Alert severity="error">{err}</Alert>}
        {msg && <Alert severity="success">{msg}</Alert>}
        <Button variant="contained" onClick={() => void submit()} disabled={!form.domain} sx={{ alignSelf: 'flex-start' }}>
          加入白名单
        </Button>
      </Stack>
    </Paper>
  );
}
