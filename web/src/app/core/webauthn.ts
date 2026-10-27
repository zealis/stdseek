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

/** 当前环境是否支持通行密钥（http 非 localhost 会被浏览器拒绝）。 */
export function passkeySupported(): boolean {
  return typeof window !== 'undefined' && !!window.PublicKeyCredential;
}
