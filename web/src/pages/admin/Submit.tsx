import { useState } from 'react';
import {
  Alert, Button, MenuItem, Paper, Stack, TextField, Typography,
} from '@mui/material';
import { addWhitelist } from '../../api';

const CATEGORIES = ['official', 'opensource', 'blog', 'platform', 'media'];

// 管理员直接提交站点进白名单（与用户提交审核队列区分）
export default function Submit() {
  const [form, setForm] = useState({ domain: '', category: 'official', crawl_interval: 86400 });
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');

  const submit = async () => {
    setErr('');
    setMsg('');
    try {
      await addWhitelist(form);
      setMsg(`${form.domain} 已加入白名单，将作为爬虫种子。`);
      setForm({ domain: '', category: 'official', crawl_interval: 86400 });
    } catch (e) {
      setErr((e as Error).message);
    }
  };

  return (
    <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper', maxWidth: 640 }}>
      <Typography variant="h6" sx={{ mb: 0.5 }}>提交站点</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        管理员提交的站点直接进入白名单。用户侧提交入口当前关闭（首批 20–50 站质量验证前不开放）。
      </Typography>
      <Stack spacing={2}>
        <TextField label="域名" placeholder="go.dev" value={form.domain}
          onChange={(e) => setForm({ ...form, domain: e.target.value.trim().toLowerCase() })} />
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
