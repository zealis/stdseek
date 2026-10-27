// 诊断：分别在 localhost 与 127.0.0.1 下尝试注册通行密钥，看浏览器报什么。
import puppeteer from 'puppeteer-core';

const b = await puppeteer.launch({ executablePath: 'C:/Program Files/Google/Chrome/Application/chrome.exe', headless: 'new' });

async function tryHost(host) {
  const p = await b.newPage();
  const client = await p.createCDPSession();
  await client.send('WebAuthn.enable');
  await client.send('WebAuthn.addVirtualAuthenticator', {
    options: { protocol: 'ctap2', transport: 'internal', hasResidentKey: true, hasUserVerification: true, isUserVerified: true, automaticPresenceSimulation: true },
  });
  await p.goto(`http://${host}/login`, { waitUntil: 'networkidle0', timeout: 20000 });
  await p.type('input[type="email"]', 'admin@example.com');
  await p.type('input[type="password"]', 'hunter2hunter2');
  await p.click('button[type="submit"]');
  await new Promise(r => setTimeout(r, 2000));

  const out = await p.evaluate(async () => {
    try {
      const r = await fetch('/api/auth/passkey/register/begin', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' });
      const data = await r.json();
      if (!r.ok) return { stage: 'begin', status: r.status, error: data.error };
      const pk = data.publicKey;
      const b64 = s => { const t = s.replace(/-/g,'+').replace(/_/g,'/'); const pad = t + '='.repeat((4-t.length%4)%4); const bin = atob(pad); const u = new Uint8Array(bin.length); for(let i=0;i<bin.length;i++)u[i]=bin.charCodeAt(i); return u.buffer; };
      const opts = { ...pk, challenge: b64(pk.challenge), user: { ...pk.user, id: b64(pk.user.id) }, excludeCredentials: (pk.excludeCredentials??[]).map(c => ({...c, id: b64(c.id)})) };
      await navigator.credentials.create({ publicKey: opts });
      return { stage: 'create', ok: true };
    } catch (e) {
      return { stage: 'create', name: e.name, message: e.message };
    }
  });
  console.log(`[${host}]`, JSON.stringify(out));
  await p.close();
}

await tryHost('localhost:8080');
await tryHost('127.0.0.1:8080');
await b.close();
