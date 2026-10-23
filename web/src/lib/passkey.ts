import { deviceName } from './push'

// passkey（R-7）：WebAuthn 的选项和结果里二进制字段都用 base64url 传，这里和浏览器的 ArrayBuffer 互转。

const enc = (buf: ArrayBuffer | null | undefined) => (buf ? btoa(String.fromCharCode(...new Uint8Array(buf))).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '') : undefined)
const dec = (s: string): ArrayBuffer => {
  const raw = atob(s.replace(/-/g, '+').replace(/_/g, '/') + '='.repeat((4 - (s.length % 4)) % 4))
  const out = new Uint8Array(new ArrayBuffer(raw.length))
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out.buffer
}

type Json = Record<string, unknown>
type CredJson = { id: string; type?: string; transports?: AuthenticatorTransport[] }

export const passkeySupported = () => typeof window !== 'undefined' && !!window.PublicKeyCredential && window.isSecureContext

async function post(path: string, body?: unknown): Promise<Json> {
  const r = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body), credentials: 'same-origin' })
  const data = (await r.json().catch(() => ({}))) as Json
  if (!r.ok) throw new Error((data.detail as string) || (data.title as string) || `HTTP ${r.status}`)
  return data
}

function credToJson(c: PublicKeyCredential): Json {
  const r = c.response as AuthenticatorAttestationResponse & AuthenticatorAssertionResponse
  const response: Json = { clientDataJSON: enc(r.clientDataJSON) }
  if ('attestationObject' in r && r.attestationObject) {
    response.attestationObject = enc(r.attestationObject)
    if (typeof r.getTransports === 'function') response.transports = r.getTransports()
  } else {
    response.authenticatorData = enc(r.authenticatorData)
    response.signature = enc(r.signature)
    response.userHandle = enc(r.userHandle)
  }
  return { id: c.id, rawId: enc(c.rawId), type: c.type, response, clientExtensionResults: c.getClientExtensionResults(), authenticatorAttachment: c.authenticatorAttachment ?? undefined }
}

const descs = (list?: CredJson[]) => list?.map((d) => ({ ...d, id: dec(d.id), type: 'public-key' as const }))

// 在已登录状态下登记一个 passkey，名字默认是当前设备
export async function registerPasskey(name = deviceName()): Promise<void> {
  const begin = await post('/api/passkeys/register/begin', { name })
  const pk = begin.publicKey as Json & { challenge: string; user: Json & { id: string }; excludeCredentials?: CredJson[] }
  const cred = (await navigator.credentials.create({
    publicKey: { ...(pk as unknown as PublicKeyCredentialCreationOptions), challenge: dec(pk.challenge), user: { ...(pk.user as unknown as PublicKeyCredentialUserEntity), id: dec(pk.user.id) }, excludeCredentials: descs(pk.excludeCredentials) },
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('已取消')
  await post('/api/passkeys/register/finish?ceremony=' + encodeURIComponent(begin.ceremony as string), credToJson(cred))
}

// 用 passkey 登录（可发现凭据：不用先输入任何东西）
export async function loginWithPasskey(): Promise<void> {
  const begin = await post('/api/auth/passkey/begin')
  const pk = begin.publicKey as Json & { challenge: string; allowCredentials?: CredJson[] }
  const cred = (await navigator.credentials.get({
    publicKey: { ...(pk as unknown as PublicKeyCredentialRequestOptions), challenge: dec(pk.challenge), allowCredentials: descs(pk.allowCredentials) },
  })) as PublicKeyCredential | null
  if (!cred) throw new Error('已取消')
  await post('/api/auth/passkey/finish?ceremony=' + encodeURIComponent(begin.ceremony as string), credToJson(cred))
}
