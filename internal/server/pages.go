// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// Small server-rendered pages next to the SPA: the web login (/login) and
// the device approval page (/device, RFC 8628 verification_uri). They use
// external scripts because the CSP forbids inline scripts.

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark"><title>{{.Title}} – TuxMail</title>
<style{{if .StyleNonce}} nonce="{{.StyleNonce}}"{{end}}>
body{font-family:system-ui,sans-serif;margin:0;min-height:100vh;display:grid;place-items:center;background:#f4f5f7;color:#111}
@media (prefers-color-scheme:dark){body{background:#111318;color:#e6e6e6}main{background:#1b1e25!important}input{background:#111318;color:#e6e6e6}}
main{background:#fff;padding:2rem;border-radius:12px;box-shadow:0 2px 16px rgba(0,0,0,.08);width:min(22rem,90vw)}
h1{font-size:1.4rem;margin:0 0 1rem}label{display:block;margin:.75rem 0 .25rem;font-size:.9rem}
input{width:100%;box-sizing:border-box;padding:.6rem;border:1px solid #9995;border-radius:8px;font-size:1rem}
button{margin-top:1.25rem;width:100%;padding:.7rem;border:0;border-radius:8px;background:#2563eb;color:#fff;font-size:1rem;cursor:pointer}
button.secondary{background:#6b7280}.err{color:#dc2626;min-height:1.2em;margin-top:.75rem}.hidden{display:none}
code{font-size:1.4rem;letter-spacing:.15em}
</style></head><body><main>{{.Body}}</main><script{{if .ScriptNonce}} nonce="{{.ScriptNonce}}"{{end}} src="{{.Script}}"></script></body></html>`))

func renderPage(w http.ResponseWriter, r *http.Request, title, script string, body template.HTML) {
	var buf bytes.Buffer
	n := noncesFrom(r)
	_ = pageTmpl.Execute(&buf, map[string]any{"Title": title, "Script": script, "Body": body, "ScriptNonce": n.script, "StyleNonce": n.style})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(buf.Bytes())
}

const loginBody = `<h1>TuxMail</h1>
<form id="login"><label for="u">Username or e-mail</label><input id="u" name="username" autocomplete="username" required autofocus>
<label for="p">Password</label><input id="p" name="password" type="password" autocomplete="current-password" required>
<div id="totp" class="hidden"><label for="t">Two-factor code</label><input id="t" name="totp" inputmode="numeric" autocomplete="one-time-code"></div>
<button type="submit" id="submit">Log in</button>
<button type="button" id="passkey" class="secondary hidden">Sign in with a passkey</button><div class="err" id="err" role="alert"></div></form>`

// commonJS reads the CSRF token and posts JSON.
const commonJS = `async function csrf(){const r=await fetch('/api/auth/csrf',{credentials:'same-origin'});return (await r.json()).csrfToken}
async function post(path,body){const t=await csrf();const r=await fetch(path,{method:'POST',credentials:'same-origin',
headers:{'Content-Type':'application/json','X-CSRF-Token':t},body:JSON.stringify(body)});
let j={};try{j=await r.json()}catch(e){}return {status:r.status,body:j}}
function msg(j){return j.message||j.error||'Error'}
`

// passkeyLoginJS (passkey_login.js) holds the WebAuthn helpers of the login page.
//
//go:embed passkey_login.js
var passkeyLoginJS string

const loginJS = `const f=document.getElementById('login'),err=document.getElementById('err');let challenge='';
function next(){const n=new URLSearchParams(location.search).get('next');if(!n||n[0]!=='/'||n[1]==='/'||n[1]==='\\')return '/';
try{const u=new URL(n,location.origin);if(u.origin===location.origin)return u.pathname+u.search+u.hash}catch(e){}return '/'}
f.addEventListener('submit',async(e)=>{e.preventDefault();err.textContent='';
const u=f.username.value,p=f.password.value,t=f.totp.value.trim();
const res=challenge?await post('/api/auth/login/totp',{challenge,code:t}):await post('/api/auth/login',{username:u,password:p});
if(res.status!==200){err.textContent=msg(res.body);return}
if(res.body.status==='mfa_required'){challenge=res.body.challenge;document.getElementById('totp').classList.remove('hidden');f.totp.focus();return}
location.href=next()});
const pkBtn=document.getElementById('passkey');
if(pkBtn){fetch('/api/auth/config',{credentials:'same-origin'}).then((r)=>r.json()).then((c)=>{if(pkShowButton(window,c))pkBtn.classList.remove('hidden')}).catch(()=>{});
pkBtn.addEventListener('click',async()=>{err.textContent='';pkBtn.disabled=true;
try{await pkSignIn(post,navigator);location.href=next()}catch(e){err.textContent=pkErrorMessage(e)}finally{pkBtn.disabled=false}})}`

const deviceBody = `<h1>Connect a device</h1>
<div id="step1"><form id="code"><label for="c">Code shown on the device</label><input id="c" name="code" autocomplete="off" required autofocus>
<button type="submit">Continue</button></form></div>
<div id="step2" class="hidden"><p>Allow <b id="name"></b> to access your TuxMail account?</p><p>Code: <code id="ucode"></code></p>
<p id="where"></p><p class="err">Only allow this if you started the sign-in on this device yourself just now. Never enter or approve a code that someone sent you.</p><div id="pw" class="hidden"><label for="p">Your password</label><input id="p" type="password" autocomplete="current-password"></div>
<div id="totp" class="hidden"><label for="t">Two-factor code</label><input id="t" inputmode="numeric" autocomplete="one-time-code"></div>
<p id="fresh" class="hidden">You just signed in with your passkey: that confirms this request.</p>
<button id="pkapprove" class="hidden">Allow with passkey</button><button id="approve">Allow</button><button id="deny" class="secondary">Deny</button></div>
<div id="done" class="hidden"><p id="donemsg"></p></div><div class="err" id="err" role="alert"></div>`

// deviceJS is the approval page. Its step-up: password (+ code), a passkey
// (#pkapprove, api_device_passkey.go) or, right after a passkey sign-in, nothing
// more (passkeyFresh).
const deviceJS = commonJS + `const $=(id)=>document.getElementById(id),err=$('err');let code='';
async function lookup(c){err.textContent='';const r=await fetch('/api/device/request?user_code='+encodeURIComponent(c),{credentials:'same-origin'});
let j={};try{j=await r.json()}catch(e){}
if(r.status===401){location.href='/login?next='+encodeURIComponent(location.pathname+'?user_code='+c);return}
if(r.status!==200){err.textContent=msg(j);return}
code=c;const q=j.request||{};$('name').textContent=q.deviceName||q.clientName||'TuxMail';$('ucode').textContent=c;
$('where').textContent=[q.ip?('IP: '+q.ip):'',q.userAgent?('Client: '+q.userAgent):'',q.createdAt?('Requested: '+new Date(q.createdAt).toLocaleString()):''].filter(Boolean).join(' · ');
if(j.passkeyFresh){$('fresh').classList.remove('hidden')}else{if(j.totpRequired)$('totp').classList.remove('hidden');
if(j.passwordRequired)$('pw').classList.remove('hidden');
if(j.passkey&&pkSupported(window)){$('pkapprove').classList.remove('hidden');$('approve').textContent='Allow with password';$('approve').classList.add('secondary')}}
$('step1').classList.add('hidden');$('step2').classList.remove('hidden')}
async function send(approve,extra){const res=await post('/api/device/approve',Object.assign({user_code:code,approve},extra));
if(res.status!==204){err.textContent=msg(res.body);return}
$('step2').classList.add('hidden');$('done').classList.remove('hidden');
$('donemsg').textContent=approve?'Done. You can return to the app.':'The request was denied.'}
$('code').addEventListener('submit',(e)=>{e.preventDefault();lookup($('c').value.trim().toUpperCase())});
function decide(approve){return send(approve,approve?{password:$('p').value,totp_code:$('t').value.trim()}:{})}
async function pkApprove(){err.textContent='';const btn=$('pkapprove');btn.disabled=true;
try{const b=await post('/api/device/approve/passkey',{});if(b.status!==200)throw Object.assign(new Error(msg(b.body)),{status:b.status});
const cred=await navigator.credentials.get({publicKey:pkRequestOptions(b.body.publicKey)});
if(!cred)throw Object.assign(new Error('cancelled'),{name:'NotAllowedError'});
await send(true,{passkey:{challengeId:b.body.challengeId,credential:pkAssertionJSON(cred)}})}
catch(e){err.textContent=pkErrorMessage(e)}finally{btn.disabled=false}}
$('pkapprove').addEventListener('click',()=>pkApprove());
$('approve').addEventListener('click',()=>decide(true));$('deny').addEventListener('click',()=>decide(false));
const pre=new URLSearchParams(location.search).get('user_code');if(pre){$('c').value=pre;lookup(pre)}`

func serveScript(w http.ResponseWriter, js string) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(js))
}

func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, "Log in", "/login.js", template.HTML(loginBody))
}

func (s *Server) handleLoginScript(w http.ResponseWriter, r *http.Request) {
	serveScript(w, commonJS+passkeyLoginJS+loginJS)
}

func (s *Server) handleDevicePage(w http.ResponseWriter, r *http.Request) {
	renderPage(w, r, "Connect a device", "/device.js", template.HTML(deviceBody))
}

func (s *Server) handleDeviceScript(w http.ResponseWriter, r *http.Request) {
	serveScript(w, passkeyLoginJS+deviceJS)
}

// loggedIn reports whether the request carries a valid session.
func (s *Server) loggedIn(r *http.Request) bool {
	_, err := s.authenticate(r)
	return err == nil
}

// loginRedirect sends browsers without session to /login (SPA navigation only).
func loginRedirect(w http.ResponseWriter, r *http.Request) {
	next := r.URL.Path
	if r.URL.RawQuery != "" {
		next += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, "/login?next="+url.QueryEscape(next), http.StatusSeeOther)
}

// injectMeta adds the transport and CSRF meta tags to index.html so that
// the SPA uses the HTTP transport against this server.
func injectMeta(idx []byte, csrf, build string, n cspNonces) []byte {
	tags := `<meta name="tuxmail-api" content="http"><meta name="tuxmail-csrf" content="` + template.HTMLEscapeString(csrf) + `">`
	if build != "" {
		tags += `<meta name="tuxmail-build" content="` + template.HTMLEscapeString(build) + `">`
	}
	if n.style != "" {
		tags += `<meta name="tuxmail-style-nonce" content="` + n.style + `">`
	}
	s := string(injectNonces(idx, n))
	if i := strings.Index(strings.ToLower(s), "<head>"); i >= 0 {
		return []byte(s[:i+6] + tags + s[i+6:])
	}
	return []byte(tags + s)
}

var (
	scriptTag     = regexp.MustCompile(`(?i)<script\b`)
	modulePreload = regexp.MustCompile(`(?i)<link\b[^>]*\brel="modulepreload"[^>]*>`)
	styleTag      = regexp.MustCompile(`(?i)<style\b`)
)

// injectNonces puts the nonces of the request on the elements of a static HTML
// file. These files come from our own build, not from user data.
func injectNonces(page []byte, n cspNonces) []byte {
	if n.script == "" {
		return page
	}
	attr := []byte(`$0 nonce="` + n.script + `"`)
	page = scriptTag.ReplaceAll(page, attr)
	page = modulePreload.ReplaceAllFunc(page, func(m []byte) []byte {
		return bytes.Replace(m, []byte("<link"), []byte(`<link nonce="`+n.script+`"`), 1)
	})
	return styleTag.ReplaceAll(page, []byte(`$0 nonce="`+n.style+`"`))
}
