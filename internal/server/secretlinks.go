// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/base64"
	"errors"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Midas1989/Mailclient/internal/auth"
	"github.com/Midas1989/Mailclient/internal/server/secretlink"
)

// One-time secret links (internal/server/secretlink). The browser of the sender encrypts the
// text with a random AES-256-GCM key; the server only sees the ciphertext. The key travels in the
// fragment of the link (https://host/secret/<id>#<key>), which browsers never send.
//
//	POST   /api/secrets             create (signed in)
//	GET    /api/secrets             own active links (signed in)
//	DELETE /api/secrets/{id}        revoke (signed in, own links)
//	POST   /api/secrets/{id}/open   public: counts a view and returns the ciphertext
//	GET    /secret/{id}             public page that asks for a click, opens and decrypts
//
// Opening is a POST on purpose: link previews of messengers and mail scanners fetch URLs with GET and
// must not use up the link. The page needs an explicit click.

const (
	secretOpenPerMinute = 20
	secretOpenBurst     = 10
	secretCreatePerMin  = 30
)

type secretCreateRequest struct {
	Ciphertext string `json:"ciphertext"` // base64url (no padding): 12 byte nonce + AES-GCM output
	TTLSeconds int64  `json:"ttlSeconds"`
	MaxViews   int    `json:"maxViews"`
}

func (s *Server) handleSecretCreate(w http.ResponseWriter, r *http.Request, a *authInfo) {
	if ok, wait := s.secretCreateLimit.allow("u" + strconv.FormatInt(a.User.ID, 10)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "throttled", "too many requests")
		return
	}
	// Read-only (guest) accounts store nothing; the check of appHandler does not cover these routes.
	if a.User.Role == auth.RoleReadonly {
		writeError(w, http.StatusForbidden, "read_only", "this account is read-only")
		return
	}
	var req secretCreateRequest
	if !readJSON(w, r, &req) {
		return
	}
	if req.TTLSeconds < 0 || req.TTLSeconds > int64(secretlink.MaxTTL/time.Second) {
		writeError(w, http.StatusBadRequest, "bad_request", "size, lifetime or number of views out of range")
		return
	}
	ct, err := base64.RawURLEncoding.DecodeString(req.Ciphertext)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid ciphertext")
		return
	}
	l, err := s.secrets.Create(r.Context(), a.User.ID, ct, time.Duration(req.TTLSeconds)*time.Second, req.MaxViews)
	switch {
	case errors.Is(err, secretlink.ErrInvalid):
		writeError(w, http.StatusBadRequest, "bad_request", "size, lifetime or number of views out of range")
		return
	case errors.Is(err, secretlink.ErrQuota):
		writeError(w, http.StatusTooManyRequests, "quota_exceeded", "too many active secret links; delete some or wait until they expire")
		return
	case err != nil:
		s.log.Error("secret link create", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not store the link")
		return
	}
	// The id is not logged (logPath masks /api/secrets/ and /secret/): with the key it is the secret.
	s.auth.Audit(r.Context(), "secretlink.create", a.User.Username, true, "")
	// url is where the recipient opens it (public_url may carry a path prefix); the browser appends #<key>.
	writeJSON(w, http.StatusCreated, struct {
		secretlink.Link
		URL string `json:"url"`
	}{l, strings.TrimRight(s.cfg.PublicURL, "/") + "/secret/" + l.ID})
}

func (s *Server) handleSecretList(w http.ResponseWriter, r *http.Request, a *authInfo) {
	ls, err := s.secrets.List(r.Context(), a.User.ID)
	if err != nil {
		s.log.Error("secret link list", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read the links")
		return
	}
	writeJSON(w, http.StatusOK, ls)
}

func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request, a *authInfo) {
	switch err := s.secrets.Delete(r.Context(), a.User.ID, r.PathValue("id")); {
	case errors.Is(err, secretlink.ErrGone):
		writeError(w, http.StatusNotFound, "gone", "no such link")
	case err != nil:
		s.log.Error("secret link delete", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not delete the link")
	default:
		s.auth.Audit(r.Context(), "secretlink.delete", a.User.Username, true, "")
		w.WriteHeader(http.StatusNoContent)
	}
}

// handleSecretOpen is public (the recipient has no account).
func (s *Server) handleSecretOpen(w http.ResponseWriter, r *http.Request) {
	if ok, wait := s.secretOpenIP.allow(limiterKey(clientFrom(r).IP)); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		writeError(w, http.StatusTooManyRequests, "throttled", "too many requests")
		return
	}
	ct, err := s.secrets.Open(r.Context(), r.PathValue("id"))
	switch {
	case errors.Is(err, secretlink.ErrGone):
		// Unknown, expired and used up look the same.
		writeError(w, http.StatusNotFound, "gone", "this link does not exist, has expired or was already used")
	case err != nil:
		s.log.Error("secret link open", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "could not read the link")
	default:
		writeJSON(w, http.StatusOK, map[string]string{"ciphertext": base64.RawURLEncoding.EncodeToString(ct)})
	}
}

const secretBody = `<h1 data-k="title">Secret message</h1>
<div id="intro"><p data-k="intro">Someone sent you a secret with a one-time link. Showing it uses up a view: it may not be possible to open it again afterwards.</p>
<button type="button" id="show" data-k="show">Show secret</button></div>
<div id="result" class="hidden"><pre id="out"></pre><button type="button" id="copy" data-k="copy">Copy</button>
<p class="note" data-k="after">Copy it now. The text is not kept in your browser, and the link in the address bar has been cleared.</p></div>
<p class="err" id="err" role="alert"></p>`

const secretJS = `(function(){
const L=(navigator.language||'en').toLowerCase().indexOf('de')===0?'de':'en';
const T={en:{title:'Secret message',intro:'Someone sent you a secret with a one-time link. Showing it uses up a view: it may not be possible to open it again afterwards.',show:'Show secret',copy:'Copy',copied:'Copied',
after:'Copy it now. The text is not kept in your browser, and the link in the address bar has been cleared.',gone:'This link does not exist, has expired or was already used.',nokey:'This link is incomplete (the part after # is missing). Ask the sender for the whole link.',
fail:'The text could not be decrypted. The link may have been shortened when it was copied.',busy:'Too many requests. Please try again in a moment.',https:'Your browser needs a secure connection (HTTPS) to decrypt this.',err:'Something went wrong. Please try again.'},
de:{title:'Geheime Nachricht',intro:'Jemand hat dir ein Geheimnis mit einem Einmal-Link geschickt. Beim Anzeigen wird ein Abruf verbraucht: Danach lässt es sich eventuell nicht mehr öffnen.',show:'Geheimnis anzeigen',copy:'Kopieren',copied:'Kopiert',
after:'Kopiere es jetzt. Der Text wird nicht im Browser gespeichert, und der Link in der Adresszeile wurde entfernt.',gone:'Dieser Link existiert nicht, ist abgelaufen oder wurde schon benutzt.',nokey:'Dieser Link ist unvollständig (der Teil nach dem # fehlt). Bitte den Absender um den ganzen Link.',
fail:'Der Text konnte nicht entschlüsselt werden. Der Link wurde beim Kopieren eventuell gekürzt.',busy:'Zu viele Anfragen. Bitte gleich noch einmal versuchen.',https:'Dein Browser braucht eine sichere Verbindung (HTTPS), um das zu entschlüsseln.',err:'Es ist etwas schiefgelaufen. Bitte noch einmal versuchen.'}}[L];
document.documentElement.lang=L;document.title=T.title+' – TuxMail';
document.querySelectorAll('[data-k]').forEach(function(e){if(T[e.dataset.k])e.textContent=T[e.dataset.k]});
const err=document.getElementById('err'),show=document.getElementById('show'),out=document.getElementById('out'),copy=document.getElementById('copy');
const id=location.pathname.split('/').pop(),keyB64=location.hash.slice(1);
function b64(s){s=s.replace(/-/g,'+').replace(/_/g,'/');while(s.length%4)s+='=';const b=atob(s),a=new Uint8Array(b.length);for(let i=0;i<b.length;i++)a[i]=b.charCodeAt(i);return a}
let keyBytes=null;try{keyBytes=b64(keyB64)}catch(e){}
if(!window.crypto||!window.crypto.subtle){err.textContent=T.https;show.disabled=true}
else if(!keyBytes||keyBytes.length!==32){err.textContent=T.nokey;show.disabled=true}
show.addEventListener('click',async function(){
err.textContent='';show.disabled=true;
try{
const res=await post('/api/secrets/'+encodeURIComponent(id)+'/open',{});
if(res.status===404){err.textContent=T.gone;return}
if(res.status===429){err.textContent=T.busy;show.disabled=false;return}
if(res.status!==200){err.textContent=T.err;show.disabled=false;return}
const data=b64(res.body.ciphertext);
const key=await crypto.subtle.importKey('raw',keyBytes,'AES-GCM',false,['decrypt']);
let plain;try{plain=await crypto.subtle.decrypt({name:'AES-GCM',iv:data.slice(0,12)},key,data.slice(12))}catch(e){err.textContent=T.fail;return}
const text=JSON.parse(new TextDecoder().decode(plain)).t;
out.textContent=String(text);
document.getElementById('intro').classList.add('hidden');document.getElementById('result').classList.remove('hidden');
history.replaceState(null,'',location.pathname);
}catch(e){err.textContent=T.err;show.disabled=false}});
copy.addEventListener('click',async function(){try{await navigator.clipboard.writeText(out.textContent);copy.textContent=T.copied}catch(e){const r=document.createRange();r.selectNodeContents(out);const s=getSelection();s.removeAllRanges();s.addRange(r)}});
})();`

func (s *Server) handleSecretPage(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Robots-Tag", "noindex, nofollow")
	renderPage(w, r, "Secret message", "/secret.js", template.HTML(secretBody))
}

func (s *Server) handleSecretScript(w http.ResponseWriter, r *http.Request) {
	serveScript(w, commonJS+secretJS)
}
