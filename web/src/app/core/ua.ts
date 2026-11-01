/**
 * 从 User-Agent 里读出「能给人看的设备名」。
 *
 * 只做到「操作系统 + 浏览器」这个粒度：UA 里的型号/版本对用户没有任何决策价值，
 * 反而会让列表变成一串技术名词。真正需要精确识别时应该靠 UA-CH 或客户端上报，
 * 但那是为了风控，不是为了给用户看。
 */
export type DeviceInfo = {
  /** 文案 key，例如 `ua.device.windows`。
   *
   * 这里返回 key 而不是中文串：设备名要跟着界面语言变，而 `parseUA` 是纯函数，
   * 在这里读 `t()` 会把语言冻结在调用那一刻。由模板/调用方翻译。 */
  deviceKey: string;
  /** 文案 key，例如 `ua.browser.chrome` */
  browserKey: string;
  kind: 'desktop' | 'mobile' | 'tablet';
};

export function parseUA(ua: string): DeviceInfo {
  const s = ua || '';

  // 先判平台。顺序要紧：Android 的 UA 里带 "Linux"，iPad 的带 "Macintosh"。
  let deviceKey = 'ua.device.unknown';
  let kind: DeviceInfo['kind'] = 'desktop';
  if (/iPad|Tablet/i.test(s) || (/Android/i.test(s) && !/Mobile/i.test(s))) {
    deviceKey = /iPad/i.test(s) ? 'ua.device.ipad' : 'ua.device.androidTablet';
    kind = 'tablet';
  } else if (/iPhone/i.test(s)) {
    deviceKey = 'ua.device.iphone';
    kind = 'mobile';
  } else if (/Android/i.test(s)) {
    deviceKey = 'ua.device.android';
    kind = 'mobile';
  } else if (/Windows/i.test(s)) {
    deviceKey = 'ua.device.windows';
  } else if (/Macintosh|Mac OS X/i.test(s)) {
    deviceKey = 'ua.device.mac';
  } else if (/Linux/i.test(s)) {
    deviceKey = 'ua.device.linux';
  } else if (/CrOS/i.test(s)) {
    deviceKey = 'ua.device.chromeos';
  }

  // 再判浏览器。Edge 的 UA 同时含 Chrome 与 Safari，Chrome 又含 Safari，所以从窄到宽匹配。
  let browserKey = 'ua.browser.other';
  if (/Edg\//i.test(s)) browserKey = 'ua.browser.edge';
  else if (/OPR\/|Opera/i.test(s)) browserKey = 'ua.browser.opera';
  else if (/Firefox\//i.test(s)) browserKey = 'ua.browser.firefox';
  else if (/Chrome\//i.test(s)) browserKey = 'ua.browser.chrome';
  else if (/Safari\//i.test(s)) browserKey = 'ua.browser.safari';

  return { deviceKey, browserKey, kind };
}
