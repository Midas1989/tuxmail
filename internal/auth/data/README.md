# Sperrliste `passwords-blocklist.txt.gz`

Daten, kein Code. Kopie aus dem Basis-Repository (`python/basis_sec/data`, Stand v2026.11.07), geprüft von `internal/auth/blocklist.go` nur bei neuen Passwörtern.

| Angabe | Wert |
|---|---|
| Quelle | SecLists, `Passwords/Common-Credentials/xato-net-10-million-passwords-1000000.txt` |
| Stand der Quelle | Commit `2f31b22e1f9963ec2fd2b12606c00f0fb04910f1`, abgerufen 2026-11-06 |
| Abruf | `https://raw.githubusercontent.com/danielmiessler/SecLists/2f31b22e1f9963ec2fd2b12606c00f0fb04910f1/Passwords/Common-Credentials/xato-net-10-million-passwords-1000000.txt` |
| SHA-256 der Quelle | `424a3e03a17df0a2bc2b3ca749d81b04e79d59cb7aeec8876a5a3f308d0caf51` |
| Lizenz | MIT, SecLists (`LICENSE.SecLists`, Copyright (c) 2018 Daniel Miessler) |
| Ursprung der Daten | „Ten Million Passwords“, Mark Burnett/xato.net, 2015; laut SecLists nach Häufigkeit sortiert. Die Datei enthält nur Passwörter, keine Benutzernamen. |
| SHA-256 dieser Datei | `e80617f6c1d54754ec864b502bbb866136a24e917b488d6ed7672bc32561ad74` |
| Umfang | 145 635 Einträge, davon 46 146 ab 12 Zeichen |

## Aufbau

- gzip mit einem Eintrag je Zeile, UTF-8, sortiert, normalisiert mit `normalize_for_blocklist`: NFKC, `casefold`, Leerraum
  am Rand entfernt. Einträge mit Steuerzeichen entfallen.
- Enthalten sind die 100 000 häufigsten verschiedenen Einträge **und** alle weiteren ab 12 Zeichen aus der Quelle. Unter
  den ersten 100 000 sind nur 511 so lang; da kürzere Passwörter schon an der Mindestlänge scheitern, wäre die Liste
  ohne die Ergänzung bei Mindestlänge 12 fast wirkungslos.

## Neu erzeugen

```bash
python -I tools/build_blocklist.py <quelle.txt> basis_sec/data/passwords-blocklist.txt.gz
```

Die Ausgabe ist reproduzierbar. Danach Herkunft und beide Prüfsummen hier anpassen; `test_blocklist.py` prüft die Summe.
