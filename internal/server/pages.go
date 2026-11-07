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
button:disabled{opacity:.5;cursor:not-allowed}button.secondary{background:#6b7280}.err{color:#dc2626;min-height:1.2em;margin-top:.75rem}.hidden{display:none}
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
let j={};try{j=await r.json()}catch(e){}return {status:r.status,body:j,retry:Number(r.headers.get('Retry-After'))||0}}
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

const deviceBody = `<h1 data-k="title">Connect a device</h1>
<div id="step1"><form id="code"><label for="c" data-k="codeLabel">Code shown on the device</label><input id="c" name="code" autocomplete="off" required autofocus>
<button type="submit" data-k="continue">Continue</button></form></div>
<div id="step2" class="hidden"><p><span data-k="allow1">Allow</span> <b id="name"></b> <span data-k="allow2">to access your TuxMail account?</span></p><p><span data-k="code">Code:</span> <code id="ucode"></code></p>
<p id="where"></p><p class="err" data-k="warn">Only allow this if you started the sign-in on this device yourself just now. Never enter or approve a code that someone sent you.</p>
<form id="proof"><div id="pw" class="hidden"><label for="p" data-k="password">Current password</label><input id="p" type="password" autocomplete="current-password"></div>
<div id="totp" class="hidden"><label for="t" data-k="totp">Two-factor code or recovery code</label><input id="t" autocomplete="one-time-code" autocapitalize="off" spellcheck="false"></div>
<div class="err" id="lock" role="alert"></div>
<button type="submit" id="approve" data-k="allow">Allow</button><button type="button" id="deny" class="secondary" data-k="deny">Deny</button></form></div>
<div id="done" class="hidden"><p id="donemsg"></p></div><div class="err" id="err" role="alert"></div>`

// deviceJS drives the approval page. Texts: English, German when the browser language is German.
// Same rules as the two-factor card in Settings: the password field stands whenever the account
// has a password, the code field whenever it has a second factor, a lock keeps the fields and
// disables the button with the remaining time until it ends.
const deviceJS = commonJS + `const $=(id)=>document.getElementById(id),err=$('err');let code='',lockTimer=0,lockEnd=0;
const L={en:{title:'Connect a device',codeLabel:'Code shown on the device',continue:'Continue',allow1:'Allow',allow2:'to access your TuxMail account?',code:'Code:',
warn:'Only allow this if you started the sign-in on this device yourself just now. Never enter or approve a code that someone sent you.',
password:'Current password',totp:'Two-factor code or recovery code',allow:'Allow',deny:'Deny',ip:'IP',client:'Client',requested:'Requested',done:'Done. You can return to the app.',denied:'The request was denied.',
wrongPw:'The password is not correct.',wrongCode:'This code is not correct or was already used.',empty:'Enter your password and your code first.',reauth:'This needs a recent sign-in. Sign in again and retry.',
locked:'Locked after too many wrong attempts. Try again in {t}. Signing in is not affected.',min:'{n} min',sec:'{n} s',unknown:'This code is unknown or has expired. Start the sign-in on the device again.',
generic:'That did not work. Please try again.',signedOut:'You are signed out. Sign in again.'},
de:{title:'Gerät verbinden',codeLabel:'Code, der auf dem Gerät angezeigt wird',continue:'Weiter',allow1:'Soll',allow2:'auf dein TuxMail-Konto zugreifen dürfen?',code:'Code:',
warn:'Erlaube das nur, wenn du die Anmeldung gerade selbst auf diesem Gerät gestartet hast. Gib nie einen Code ein und bestätige nie einen, den dir jemand geschickt hat.',
password:'Aktuelles Passwort',totp:'Zwei-Faktor-Code oder Wiederherstellungscode',allow:'Erlauben',deny:'Ablehnen',ip:'IP',client:'Programm',requested:'Angefragt',done:'Fertig. Du kannst zur App zurückkehren.',denied:'Die Anfrage wurde abgelehnt.',
wrongPw:'Das Passwort stimmt nicht.',wrongCode:'Dieser Code stimmt nicht oder wurde schon benutzt.',empty:'Gib zuerst dein Passwort und deinen Code ein.',reauth:'Dafür ist eine frische Anmeldung nötig. Melde dich neu an und versuche es noch einmal.',
locked:'Gesperrt nach zu vielen falschen Eingaben. Neuer Versuch in {t}. Das Anmelden ist nicht betroffen.',min:'{n} Minuten',sec:'{n} Sekunden',unknown:'Dieser Code ist unbekannt oder abgelaufen. Starte die Anmeldung am Gerät neu.',
generic:'Das hat nicht geklappt. Bitte versuche es noch einmal.',signedOut:'Du bist abgemeldet. Melde dich neu an.'}};
const tx=((navigator.language||'en').toLowerCase().startsWith('de')?L.de:L.en);
document.documentElement.lang=tx===L.de?'de':'en';document.title=tx.title+' – TuxMail';
document.querySelectorAll('[data-k]').forEach((e)=>{if(tx[e.dataset.k])e.textContent=tx[e.dataset.k]});
function why(res){const b=res.body||{},c=b.error;
if(res.status===429||c==='throttled')return null;
if(c==='invalid_credentials')return tx.wrongPw;if(c==='invalid_code')return tx.wrongCode;if(c==='reauth_required')return (b.message||'').indexOf('enter your')===0?tx.empty:tx.reauth;
if(c==='not_found')return tx.unknown;if(res.status===401)return tx.signedOut;return tx===L.de?tx.generic:msg(b)}
function lockFor(sec){clearInterval(lockTimer);lockEnd=Date.now()+Math.max(1,sec)*1000;
const tick=()=>{const left=Math.ceil((lockEnd-Date.now())/1000);
if(left<=0){clearInterval(lockTimer);$('lock').textContent='';$('approve').disabled=false;return}
$('approve').disabled=true;const t=left>=60?tx.min.replace('{n}',Math.ceil(left/60)):tx.sec.replace('{n}',left);$('lock').textContent=tx.locked.replace('{t}',t)};
tick();lockTimer=setInterval(tick,500)}
async function lookup(c){err.textContent='';const r=await fetch('/api/device/request?user_code='+encodeURIComponent(c),{credentials:'same-origin'});
let j={};try{j=await r.json()}catch(e){}
if(r.status===401){location.href='/login?next='+encodeURIComponent(location.pathname+'?user_code='+c);return}
if(r.status!==200){err.textContent=why({status:r.status,body:j})||msg(j);return}
code=c;const q=j.request||{};$('name').textContent=q.deviceName||q.clientName||'TuxMail';$('ucode').textContent=c;
$('where').textContent=[q.ip?(tx.ip+': '+q.ip):'',q.userAgent?(tx.client+': '+q.userAgent):'',q.createdAt?(tx.requested+': '+new Date(q.createdAt).toLocaleString()):''].filter(Boolean).join(' · ');
if(j.totpRequired)$('totp').classList.remove('hidden');
if(j.passwordRequired)$('pw').classList.remove('hidden');
$('step1').classList.add('hidden');$('step2').classList.remove('hidden')}
async function decide(approve){err.textContent='';const v=$('t').value.trim(),six=v.replace(/[\s-]/g,'');
const body={user_code:code,approve,password:$('p').value};if(/^\d{6}$/.test(six))body.totp_code=six;else if(v)body.recovery_code=v;
$('approve').disabled=true;const res=await post('/api/device/approve',body);if(Date.now()>=lockEnd)$('approve').disabled=false;
if(res.status!==204){if(res.status===429||res.body.error==='throttled')lockFor(res.retry||60);else err.textContent=why(res)||tx.generic;$('t').value='';return}
$('step2').classList.add('hidden');$('done').classList.remove('hidden');
$('donemsg').textContent=approve?tx.done:tx.denied}
$('code').addEventListener('submit',(e)=>{e.preventDefault();lookup($('c').value.trim().toUpperCase())});
$('proof').addEventListener('submit',(e)=>{e.preventDefault();decide(true)});$('deny').addEventListener('click',()=>decide(false));
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

func (s *Server) handleDeviceScript(w http.ResponseWriter, r *http.Request) { serveScript(w, deviceJS) }

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
