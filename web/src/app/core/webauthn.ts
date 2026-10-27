/**
 * WebAuthn 的浏览器侧胶水：base64url ↔ ArrayBuffer 的来回转换。
 *
 * 为什么必须转：服务端（go-webauthn）用 base64url 字符串传二进制，而浏览器 API
 * 只接受 ArrayBuffer。这一层出错的表现是「浏览器抛 TypeError，什么都不发生」，
 * 所以两个方向都要写清楚、都要有对称的实现。
 */

/** base64url 字符串 → ArrayBuffer */
export function b64urlToBuf(s: string): ArrayBuffer {
  const b64 = s.replace(/-/g, '+').replace(/_/g, '/');
  const padded = b64 + '='.repeat((4 - (b64.length % 4)) % 4);
  const bin = atob(padded);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return buf.buffer;
}

/** ArrayBuffer → base64url（无 padding） */
export function bufToB64url(buf: ArrayBuffer): string {
  const bytes = new Uint8Array(buf);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** 服务端下发的注册选项 → 浏览器需要的结构 */
export function toCreationOptions(raw: any): PublicKeyCredentialCreationOptions {
  return {
    ...raw,
    challenge: b64urlToBuf(raw.challenge),
    user: { ...raw.user, id: b64urlToBuf(raw.user.id) },
    excludeCredentials: (raw.excludeCredentials ?? []).map((c: any) => ({
      ...c,
      id: b64urlToBuf(c.id),
    })),
  } as PublicKeyCredentialCreationOptions;
}

/** 服务端下发的登录选项 → 浏览器需要的结构 */
export function toRequestOptions(raw: any): PublicKeyCredentialRequestOptions {
  return {
    ...raw,
    challenge: b64urlToBuf(raw.challenge),
    allowCredentials: (raw.allowCredentials ?? []).map((c: any) => ({
      ...c,
      id: b64urlToBuf(c.id),
    })),
  } as PublicKeyCredentialRequestOptions;
}

/** 注册响应 → 可 JSON 化的结构（原样交给服务端解析） */
export function serializeAttestation(cred: PublicKeyCredential): unknown {
  const r = cred.response as AuthenticatorAttestationResponse;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      attestationObject: bufToB64url(r.attestationObject),
      transports: typeof r.getTransports === 'function' ? r.getTransports() : [],
    },
  };
}

/** 登录响应 → 可 JSON 化的结构 */
export function serializeAssertion(cred: PublicKeyCredential): unknown {
  const r = cred.response as AuthenticatorAssertionResponse;
  return {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults(),
    response: {
      clientDataJSON: bufToB64url(r.clientDataJSON),
      authenticatorData: bufToB64url(r.authenticatorData),
      signature: bufToB64url(r.signature),
      userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
    },
  };
}

/**
 * 当前环境能否用通行密钥。
 *
 * 两个硬条件：浏览器支持 WebAuthn，且页面处于安全上下文（https 或 localhost）。
 * 用 IP 访问（127.0.0.1 以外的内网地址）虽然算安全上下文，但 RPID 必须是有效域名，
 * 浏览器会直接拒绝——与其让用户对着「添加失败」发懵，不如提前说清楚。
 */
export function passkeySupported(): boolean {
  if (typeof window === 'undefined' || !window.PublicKeyCredential) return false;
  if (location.protocol === 'https:') return true;
  return location.hostname === 'localhost' || location.hostname === '127.0.0.1';
}

/**
 * 把 WebAuthn 抛出的 DOMException 翻成人话。
 *
 * 浏览器原始的 message 是给开发者看的（NotAllowedError 之类），直接展示给用户
 * 等于没说。这几个 name 是实际会遇到的全部情况。
 */
export function passkeyErrorText(e: unknown): string {
  if (e instanceof DOMException) {
    switch (e.name) {
      case 'NotAllowedError':
        return '操作已取消或超时。请重试，并在设备上完成指纹 / 面容 / PIN 验证';
      case 'InvalidStateError':
        return '这台设备已经登记过通行密钥了，可以在列表里查看';
      case 'NotSupportedError':
        return '当前环境不支持通行密钥（需要 HTTPS 或 localhost）';
      case 'SecurityError':
        return '域名不匹配：请用登记通行密钥时的地址访问';
      case 'AbortError':
        return '操作被中断，请重试';
      case 'ConstraintError':
        return '设备不满足要求（可能需要带生物识别的设备）';
      default:
        return `${e.name}：${e.message}`;
    }
  }
  return e instanceof Error ? e.message : '操作失败，请重试';
}
