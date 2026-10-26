import { useCallback, useEffect, useState } from 'react';
import {
  Alert, Box, Chip, Divider, FormControlLabel, Paper, Stack, Switch, Typography,
} from '@mui/material';
import { listFeatures, setFeature, type FeatureFlag } from '../../api';

// 功能开关：只做「改这个开关」一件事。
// 刻意不做「保存」按钮——开关这类东西，多一次点击就多一次"以为改了其实没改"，
// 点下去即生效，状态以服务端返回为准（失败时回滚显示）。
export default function Features() {
  const [items, setItems] = useState<FeatureFlag[]>([]);
  const [err, setErr] = useState('');
  const [msg, setMsg] = useState('');
  const [busy, setBusy] = useState('');
  const [loaded, setLoaded] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await listFeatures();
      setItems(r.features ?? []);
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const toggle = async (f: FeatureFlag, on: boolean) => {
    setBusy(f.key);
    setErr('');
    setMsg('');
    try {
      await setFeature(f.key, on);
      setMsg(`${f.label}已${on ? '开启' : '关闭'}，立即生效。`);
      await load();
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy('');
    }
  };

  return (
    <>
      <Typography variant="h6" sx={{ mb: 1 }}>功能开关</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        开关存在数据库里，改完立即生效，不需要重启服务。关闭的功能不会占用任何调用成本。
      </Typography>

      {err && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setErr('')}>{err}</Alert>}
      {msg && <Alert severity="success" sx={{ mb: 2 }} onClose={() => setMsg('')}>{msg}</Alert>}

      <Paper variant="outlined" sx={{ bgcolor: 'background.paper' }}>
        {!loaded && <Box sx={{ p: 3 }}><Typography variant="body2" color="text.secondary">加载中…</Typography></Box>}
        {loaded && items.length === 0 && (
          <Box sx={{ p: 3 }}><Typography variant="body2" color="text.secondary">没有可配置的开关。</Typography></Box>
        )}
        {items.map((f, i) => (
          <Box key={f.key}>
            {i > 0 && <Divider />}
            <Box sx={{ p: 2.5 }}>
              <FormControlLabel
                control={
                  <Switch
                    checked={f.enabled}
                    disabled={busy === f.key}
                    onChange={(e) => void toggle(f, e.target.checked)}
                    color={f.danger ? 'warning' : 'primary'}
                  />
                }
                label={
                  <Stack direction="row" spacing={1} alignItems="center" sx={{ flexWrap: 'wrap' }}>
                    <Typography variant="subtitle1" sx={{ fontWeight: 600 }}>{f.label}</Typography>
                    {f.danger && <Chip size="small" color="warning" variant="outlined" label="涉及模型调用成本" />}
                    <Chip
                      size="small"
                      variant="outlined"
                      color={f.enabled ? 'success' : 'default'}
                      label={f.enabled ? '运行中' : '已关闭'}
                    />
                  </Stack>
                }
                sx={{ alignItems: 'flex-start', m: 0, '& .MuiSwitch-root': { mt: 0.25 } }}
              />
              <Typography variant="body2" color="text.secondary" sx={{ mt: 1, ml: 6, lineHeight: 1.7 }}>
                {f.desc}
              </Typography>
              <Typography variant="caption" color="text.secondary" sx={{ ml: 6, display: 'block', mt: 0.5 }}>
                {f.updated_at
                  ? `最后修改：${new Date(f.updated_at * 1000).toLocaleString()}`
                  : '从未修改过，当前是默认值'}
              </Typography>
            </Box>
          </Box>
        ))}
      </Paper>

      <Alert severity="info" variant="outlined" sx={{ mt: 3 }}>
        开关变更会写入服务日志（feature flag changed），便于事后回溯某天为什么行为不同。
      </Alert>
    </>
  );
}
