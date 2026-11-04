#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
#
# Starts a throw-away Dovecot (IMAP 143/STARTTLS + IMAPS 993, LMTP, SASL) and
# Postfix (SMTP 25, submission 587 with AUTH) for manual tests against real
# mail servers (docs/ENTWICKLUNG.md). Needs root and the Debian/Ubuntu
# packages dovecot-imapd dovecot-lmtpd postfix openssl.
#
#   sudo scripts/dovecot-dev.sh start [DIR]   # default DIR /tmp/tuxmail-dovecot
#   sudo scripts/dovecot-dev.sh stop  [DIR]
#
# Users: anna@tuxmail.test and ben@tuxmail.test, password "tuxmail-dev".
# The self-signed CA is DIR/ca.pem: start TuxMail with SSL_CERT_FILE=DIR/ca.pem
# so that TLS connections to 127.0.0.1/localhost are verified.
set -eu
cmd=${1:-start}
D=${2:-/tmp/tuxmail-dovecot}
DOMAIN=tuxmail.test
PW=tuxmail-dev

stop() {
	[ -f "$D/dovecot.conf" ] && dovecot -c "$D/dovecot.conf" stop 2>/dev/null || true
	[ -d "$D/postfix" ] && postfix -c "$D/postfix" stop 2>/dev/null || true
}

if [ "$cmd" = stop ]; then
	stop
	exit 0
fi

stop
rm -rf "$D/mail" # throw-away mailboxes: every start begins empty
mkdir -p "$D/mail" "$D/run" "$D/postfix" "$D/spool"
id vmail >/dev/null 2>&1 || useradd -r -d "$D/mail" -s /usr/sbin/nologin vmail
chown -R vmail:vmail "$D/mail"

# Self-signed CA + server certificate for localhost / 127.0.0.1.
if [ ! -f "$D/server.pem" ]; then
	openssl req -x509 -newkey rsa:2048 -nodes -days 30 -subj "/CN=TuxMail dev CA" \
		-keyout "$D/ca.key" -out "$D/ca.pem" 2>/dev/null
	openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout "$D/server.key" -out "$D/server.csr" 2>/dev/null
	printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\n' > "$D/san.ext"
	openssl x509 -req -in "$D/server.csr" -CA "$D/ca.pem" -CAkey "$D/ca.key" -CAcreateserial -days 30 \
		-extfile "$D/san.ext" -out "$D/server.pem" 2>/dev/null
	chmod 644 "$D/server.key"
fi

HASH=$(doveadm pw -s SHA512-CRYPT -p "$PW")
: > "$D/users"
for u in anna ben; do
	echo "$u@$DOMAIN:$HASH:5000:5000::$D/mail/$u::" >> "$D/users"
done

cat > "$D/dovecot.conf" <<EOF
base_dir = $D/run
protocols = imap lmtp
listen = 127.0.0.1
log_path = $D/dovecot.log
ssl = yes
ssl_cert = <$D/server.pem
ssl_key = <$D/server.key
disable_plaintext_auth = no
auth_mechanisms = plain login
mail_location = maildir:~/Maildir
mail_uid = vmail
mail_gid = vmail
first_valid_uid = 1
passdb {
  driver = passwd-file
  args = scheme=SHA512-CRYPT $D/users
}
userdb {
  driver = passwd-file
  args = $D/users
}
namespace inbox {
  inbox = yes
  separator = /
  mailbox Sent {
    special_use = \\Sent
    auto = subscribe
  }
  mailbox Drafts {
    special_use = \\Drafts
    auto = subscribe
  }
  mailbox Archive {
    special_use = \\Archive
    auto = subscribe
  }
  mailbox Trash {
    special_use = \\Trash
    auto = subscribe
  }
  mailbox Junk {
    special_use = \\Junk
    auto = subscribe
  }
}
service imap-login {
  inet_listener imap {
    port = 143
  }
  inet_listener imaps {
    port = 993
    ssl = yes
  }
}
service lmtp {
  unix_listener lmtp {
    mode = 0666
  }
}
service auth {
  unix_listener auth-postfix {
    mode = 0666
  }
}
protocol imap {
  mail_plugins = \$mail_plugins
  imap_idle_notify_interval = 1 mins
}
EOF
dovecot -c "$D/dovecot.conf" </dev/null >/dev/null 2>&1

# Postfix: virtual domain $DOMAIN delivered via Dovecot LMTP, submission with SASL.
cp /etc/postfix/master.cf "$D/postfix/master.cf"
cat >> "$D/postfix/master.cf" <<EOF
587       inet  n       -       n       -       -       smtpd
  -o syslog_name=postfix/submission
  -o smtpd_sasl_auth_enable=yes
  -o smtpd_tls_security_level=may
  -o smtpd_client_restrictions=permit_sasl_authenticated,reject
  -o smtpd_recipient_restrictions=permit_sasl_authenticated,reject
EOF
cat > "$D/postfix/main.cf" <<EOF
compatibility_level = 3.6
queue_directory = $D/spool
data_directory = $D/spool/data
myhostname = mail.$DOMAIN
mydestination =
inet_interfaces = 127.0.0.1
inet_protocols = ipv4
mynetworks = 127.0.0.0/8
virtual_mailbox_domains = $DOMAIN
virtual_transport = lmtp:unix:$D/run/lmtp
smtpd_sasl_type = dovecot
smtpd_sasl_path = $D/run/auth-postfix
smtpd_tls_cert_file = $D/server.pem
smtpd_tls_key_file = $D/server.key
smtpd_relay_restrictions = permit_mynetworks, permit_sasl_authenticated, reject_unauth_destination
maillog_file_prefixes = /tmp, /var
maillog_file = $D/postfix.log
EOF
mkdir -p "$D/spool/data"
chown postfix "$D/spool/data"
postfix -c "$D/postfix" set-permissions </dev/null >/dev/null 2>&1 || true
# No chroot: the Dovecot sockets live outside the queue directory.
postconf -c "$D/postfix" -F '*/*/chroot = n'
postfix -c "$D/postfix" start </dev/null >/dev/null 2>&1
echo "Dovecot + Postfix running in $D (IMAP 143/993, SMTP 25, submission 587). CA: $D/ca.pem"
