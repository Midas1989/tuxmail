// Passkey sign-in of the server-rendered login page (pages.go). This file is
// the middle part of /login.js: common helpers (csrf, post, msg) + this +
// the form logic. Plain script, no modules (the CSP only allows 'self' scripts).
// frontend/src/lib/passkeys/loginjs.test.ts runs it in a sandbox.
// The SPA has its own typed copy of the encoding helpers
// (frontend/src/lib/passkeys/webauthn.ts); both are tested with the same vectors.

function pkB64urlToBytes(s) {
  s = String(s).replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4) s += '=';
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function pkBytesToB64url(buf) {
  const b = new Uint8Array(buf);
  let s = '';
  for (let i = 0; i < b.length; i++) s += String.fromCharCode(b[i]);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// Server options (JSON, base64url) -> argument of navigator.credentials.get({publicKey}).
function pkRequestOptions(o) {
  const out = {
    challenge: pkB64urlToBytes(o.challenge),
    rpId: o.rpId,
    timeout: o.timeout,
    userVerification: o.userVerification || 'required',
    allowCredentials: (o.allowCredentials || []).map(function (c) {
      return { type: c.type, id: pkB64urlToBytes(c.id), transports: c.transports };
    }),
  };
  return out;
}

// PublicKeyCredential (assertion) -> the JSON the server expects.
function pkAssertionJSON(cred) {
  const r = cred.response;
  const out = {
    id: cred.id,
    rawId: pkBytesToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: typeof cred.getClientExtensionResults === 'function' ? cred.getClientExtensionResults() : {},
    response: {
      clientDataJSON: pkBytesToB64url(r.clientDataJSON),
      authenticatorData: pkBytesToB64url(r.authenticatorData),
      signature: pkBytesToB64url(r.signature),
    },
  };
  if (cred.authenticatorAttachment) out.authenticatorAttachment = cred.authenticatorAttachment;
  if (r.userHandle && r.userHandle.byteLength) out.response.userHandle = pkBytesToB64url(r.userHandle);
  return out;
}

// Passkeys need a secure context and the WebAuthn API.
function pkSupported(env) {
  return !!(env && env.PublicKeyCredential && env.isSecureContext !== false && env.navigator && env.navigator.credentials && typeof env.navigator.credentials.get === 'function');
}

// Whether the button is shown: browser support AND the server announces passkeys.
function pkShowButton(env, config) {
  return pkSupported(env) && !!(config && config.passkeysEnabled === true);
}

function pkErrorMessage(err) {
  const name = err && err.name;
  if (name === 'NotAllowedError' || name === 'AbortError') return 'The passkey sign-in was cancelled or timed out.';
  if (name === 'SecurityError') return 'Passkeys do not work on this address.';
  if (name === 'NotSupportedError') return 'This device does not support passkeys.';
  if (err && err.status === 429) return 'Too many attempts. Please wait a moment.';
  if (err && err.message) return err.message;
  return 'The passkey sign-in failed.';
}

// begin -> browser dialog -> finish. post(path, body) is the helper of the
// login page: {status, body}. Resolves with the finish response body or throws
// an Error (with .status for HTTP failures).
async function pkSignIn(post, nav) {
  const b = await post('/api/auth/passkeys/login/begin', {});
  if (b.status !== 200) throw Object.assign(new Error(msg(b.body)), { status: b.status });
  const cred = await nav.credentials.get({ publicKey: pkRequestOptions(b.body.publicKey) });
  if (!cred) throw Object.assign(new Error('cancelled'), { name: 'NotAllowedError' });
  const f = await post('/api/auth/passkeys/login/finish', { challengeId: b.body.challengeId, credential: pkAssertionJSON(cred) });
  if (f.status !== 200) throw Object.assign(new Error(msg(f.body)), { status: f.status });
  return f.body;
}
