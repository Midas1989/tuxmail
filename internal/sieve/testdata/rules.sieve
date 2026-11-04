# BEGIN TuxMail (managed by TuxMail; changes inside this section are overwritten)
require ["copy", "fileinto", "imap4flags"];
# BEGIN part rules
# requires: copy,fileinto,imap4flags
# workflow big: Große Mails
if size :over 10485760 {
  addflag "Gross";
  redirect :copy "archiv@example.org";
}
# workflow invoices: Rechnungen
if allof(address :domain :is "from" "telekom.de", anyof(header :contains "subject" "Rechnung", header :contains "content-type" "multipart/mixed"), not header :is "x-spam" "yes") {
  fileinto "Rechnungen/Telekom";
  addflag "\\Seen";
}
# workflow news: Newsletter
if address :all :matches "from" "*@news.example.com" {
  discard;
}
# END part rules
# END TuxMail
