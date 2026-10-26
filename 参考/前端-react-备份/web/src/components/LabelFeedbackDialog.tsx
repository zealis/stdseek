import { useState } from 'react';
import {
  Alert, Button, Checkbox, Dialog, DialogActions, DialogContent, DialogTitle,
  FormControlLabel, Stack, TextField, Typography,
} from '@mui/material';
import { submitLabelFeedback } from '../api';

// 标签中文名与解释。raw label 是索引里的机器名（ai_suspect 等），
// 直接展示给用户等于让用户猜，也谈不上「可解释」。
export const LABEL_TEXT: Record<string, string> = {
  ai_suspect: '疑似 AI 生成',
  ad_heavy: '广告较多',
  personal: '个人站点',
  ugc_platform: 'UGC 平台',
};

export function labelText(label: string) {
  return LABEL_TEXT[label] ?? label;
}

// 标签误判反馈。规划书红线：概率性标签必须可解释 + 可反馈，
// 所以弹窗里把判定理由一起摊开，用户看着理由判断，而不是只看结果猜。
export default function LabelFeedbackDialog({
  open, onClose, url, labels, reasons,
}: {
  open: boolean;
  onClose: () => void;
  url: string;
  labels: string[];
  reasons: Record<string, string> | null;
}) {
  const [wrong, setWrong] = useState<string[]>([]);
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState('');
  const [err, setErr] = useState('');

  const reset = () => {
    setWrong([]);
    setNote('');
    setMsg('');
    setErr('');
  };

  const close = () => {
    reset();
    onClose();
  };

  const send = async () => {
    setBusy(true);
    setErr('');
    setMsg('');
    try {
      // 没勾任何标签 = 全部认可，记一条 confirmed 用于看规则精确率；
      // 勾了就只对勾选的那些报误判，没勾的不表态——不该替用户说「没问题」。
      const targets = wrong.length > 0 ? wrong : labels;
      const verdict = wrong.length > 0 ? 'false_positive' : 'confirmed';
      for (const label of targets) {
        await submitLabelFeedback({ url, label, verdict, note: note.trim() || undefined });
      }
      setMsg(wrong.length > 0
        ? `已记录 ${wrong.length} 个误判，感谢反馈。规则调整后会重新计算标签。`
        : '已记录为「标签判定正确」。');
      setWrong([]);
      setNote('');
    } catch (e) {
      setErr((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog open={open} onClose={close} maxWidth="sm" fullWidth>
      <DialogTitle sx={{ fontSize: '1rem', pb: 0.5 }}>
        标签反馈
        <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 0.5 }}>
          标签是按规则自动判定的，会出错。勾选你认为不成立的标签，我们会据此调整规则。
        </Typography>
      </DialogTitle>
      <DialogContent dividers>
        <Stack spacing={1}>
          {labels.map((l) => (
            <Stack key={l} spacing={0.25}>
              <FormControlLabel
                control={
                  <Checkbox
                    size="small"
                    checked={wrong.includes(l)}
                    onChange={(e) => setWrong(e.target.checked
                      ? [...wrong, l]
                      : wrong.filter((x) => x !== l))}
                  />
                }
                label={<Typography variant="body2">{labelText(l)} 不成立</Typography>}
                sx={{ ml: 0 }}
              />
              <Typography variant="caption" color="text.secondary" sx={{ pl: 4 }}>
                判定理由：{reasons?.[l] || '（无详情，按站点特征归类）'}
              </Typography>
            </Stack>
          ))}
          <TextField
            size="small"
            label="补充说明（可选）"
            placeholder="例如：这是我自己写的原创文章，没有用 AI"
            value={note}
            onChange={(e) => setNote(e.target.value)}
            multiline
            minRows={2}
            inputProps={{ maxLength: 300 }}
            sx={{ mt: 1 }}
          />
          {err && <Alert severity="error">{err}</Alert>}
          {msg && <Alert severity="success">{msg}</Alert>}
        </Stack>
      </DialogContent>
      <DialogActions sx={{ px: 2, py: 1 }}>
        <Button size="small" onClick={close}>关闭</Button>
        <Button size="small" variant="contained" onClick={() => void send()} disabled={busy}
          sx={{ minWidth: 0, px: 2 }}>
          {busy ? '提交中…' : wrong.length > 0 ? `提交 ${wrong.length} 个误判` : '确认标签无误'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}
