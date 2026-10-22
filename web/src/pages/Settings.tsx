import { useState } from 'react';
import {
  Alert, Box, Button, Container, Divider, FormControlLabel, Paper, Stack, Switch, Typography,
} from '@mui/material';
import { FILTERS, TIME_WINDOWS, loadPrefs, savePrefs } from '../prefs';

export default function Settings() {
  const [prefs, setPrefs] = useState(loadPrefs());
  const [saved, setSaved] = useState(false);

  const toggle = (key: string) => {
    setPrefs((p) => ({
      ...p,
      exclude: p.exclude.includes(key) ? p.exclude.filter((x) => x !== key) : [...p.exclude, key],
    }));
    setSaved(false);
  };

  const save = () => {
    savePrefs(prefs);
    setSaved(true);
  };

  return (
    <Container sx={{ pt: 4, pb: 6 }}>
      <Typography variant="h5" sx={{ mb: 1 }}>搜索偏好</Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 3 }}>
        偏好保存在本机（localStorage），搜索结果页默认按这里的设置过滤。
      </Typography>

      <Paper variant="outlined" sx={{ p: 3, bgcolor: 'background.paper' }}>
        <Typography variant="subtitle2" sx={{ mb: 1 }}>默认排除的内容成分</Typography>
        <Stack>
          {FILTERS.map((f) => (
            <FormControlLabel
              key={f.key}
              control={<Switch checked={prefs.exclude.includes(f.key)} onChange={() => toggle(f.key)} />}
              label={
                <Box>
                  <Typography variant="body2">{f.label}</Typography>
                  <Typography variant="caption" color="text.secondary">{f.hint}</Typography>
                </Box>
              }
              sx={{ mb: 1, alignItems: 'flex-start', '& .MuiSwitch-root': { mt: 0.5 } }}
            />
          ))}
        </Stack>

        <Divider sx={{ my: 2 }} />

        <Typography variant="subtitle2" sx={{ mb: 1 }}>默认时间范围</Typography>
        <Stack direction="row" spacing={1}>
          {TIME_WINDOWS.map((t) => (
            <Button
              key={t.since}
              size="small"
              variant={prefs.since === t.since ? 'contained' : 'outlined'}
              onClick={() => { setPrefs((p) => ({ ...p, since: t.since })); setSaved(false); }}
            >
              {t.label}
            </Button>
          ))}
        </Stack>

        <Box sx={{ mt: 3 }}>
          <Button variant="contained" onClick={save}>保存偏好</Button>
          {saved && <Alert severity="success" sx={{ mt: 2 }}>已保存到本机。</Alert>}
        </Box>
      </Paper>

      <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 3 }}>
        说明：`no AI` 标签只做过滤开关，不参与排序降权；判定为概率性，结果页会给出打标理由。
      </Typography>
    </Container>
  );
}
