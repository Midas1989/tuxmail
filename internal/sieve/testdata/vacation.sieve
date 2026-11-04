# BEGIN TuxMail (managed by TuxMail; changes inside this section are overwritten)
require ["date", "relational", "vacation"];
# BEGIN part vacation
# requires: date,relational,vacation
# out-of-office reply
if allof(currentdate :zone "+0100" :value "ge" "date" "2026-12-20", currentdate :zone "+0100" :value "lt" "date" "2027-01-06") {
  vacation :days 7 :subject "Nicht im Büro" :addresses ["anna@example.org", "a@example.org"] text:
Hallo,
ich bin bis zum 6. Januar nicht erreichbar.
..Punkt am Anfang
.
;
}
# END part vacation
# END TuxMail
