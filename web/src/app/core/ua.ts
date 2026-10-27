/**
 * 从 User-Agent 里读出「能给人看的设备名」。
 *
 * 只做到「操作系统 + 浏览器」这个粒度：UA 里的型号/版本对用户没有任何决策价值，
 * 反而会让列表变成一串技术名词。真正需要精确识别时应该靠 UA-CH 或客户端上报，
 * 但那是为了风控，不是为了给用户看。
 */
export type DeviceInfo = {
  /** 例如「Windows 电脑」「iPhone」 */
  device: string;
  /** 例如「Google Chrome」 */
  browser: string;
  kind: 'desktop' | 'mobile' | 'tablet';
};

export function parseUA(ua: string): DeviceInfo {
  const s = ua || '';

  // 先判平台。顺序要紧：Android 的 UA 里带 "Linux"，iPad 的带 "Macintosh"。
  let device = '未知设备';
  let kind: DeviceInfo['kind'] = 'desktop';
  if (/iPad|Tablet/i.test(s) || (/Android/i.test(s) && !/Mobile/i.test(s))) {
    device = /iPad/i.test(s) ? 'iPad' : 'Android 平板';
    kind = 'tablet';
  } else if (/iPhone/i.test(s)) {
    device = 'iPhone';
    kind = 'mobile';
  } else if (/Android/i.test(s)) {
    device = 'Android 手机';
    kind = 'mobile';
  } else if (/Windows/i.test(s)) {
    device = 'Windows 电脑';
  } else if (/Macintosh|Mac OS X/i.test(s)) {
    device = 'Mac';
  } else if (/Linux/i.test(s)) {
    device = 'Linux 电脑';
  } else if (/CrOS/i.test(s)) {
    device = 'ChromeOS';
  }

  // 再判浏览器。Edge 的 UA 同时含 Chrome 与 Safari，Chrome 又含 Safari，所以从窄到宽匹配。
  let browser = '其他浏览器';
  if (/Edg\//i.test(s)) browser = 'Microsoft Edge';
  else if (/OPR\/|Opera/i.test(s)) browser = 'Opera';
  else if (/Firefox\//i.test(s)) browser = 'Firefox';
  else if (/Chrome\//i.test(s)) browser = 'Google Chrome';
  else if (/Safari\//i.test(s)) browser = 'Safari';

  return { device, browser, kind };
}
