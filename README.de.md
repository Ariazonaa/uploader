<div align="center">

# Uploader

**Selbst gehosteter Datei- und Bild-Host für ShareX und den Browser. Ein Go-Binary, ein Docker-Image.**

[![CI](https://github.com/ariazonaa/uploader/actions/workflows/ci.yml/badge.svg)](https://github.com/ariazonaa/uploader/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/ariazonaa/uploader?filename=backend%2Fgo.mod)](backend/go.mod)
[![Release](https://img.shields.io/github/v/release/ariazonaa/uploader)](https://github.com/ariazonaa/uploader/releases/latest)
[![Image](https://img.shields.io/badge/ghcr.io-ariazonaa%2Fuploader-2496ED?logo=docker&logoColor=white)](https://github.com/ariazonaa/uploader/pkgs/container/uploader)

[English](README.md) | Deutsch

</div>

<p align="center">
  <img src="docs/screenshots/dashboard.webp" alt="Dashboard mit hochgeladenen Fotos, Statistiken und Filtern" width="900">
</p>

Uploader ist ein Upload-Server im Stil von Zipline. Dateien kommen per ShareX, curl,
Flameshot oder einem beliebigen HTTP-Client herein oder werden einfach ins
Web-Dashboard gezogen. Zurück gibt es einen kurzen Link, der in Discord, Slack,
Mastodon und Co. eine saubere Vorschau erzeugt. Das Go-Backend liefert JSON-API,
öffentliche Share-Seiten und das eingebettete React-Dashboard aus einem einzigen
Binary aus. Die einzige Abhängigkeit ist PostgreSQL.

<p align="center">
  <a href="#schnellstart-docker-compose"><b>Schnellstart</b></a> ·
  <a href="#funktionen"><b>Funktionen</b></a> ·
  <a href="#screenshots"><b>Screenshots</b></a> ·
  <a href="#sharex-und-api"><b>ShareX & API</b></a> ·
  <a href="#konfiguration"><b>Konfiguration</b></a> ·
  <a href="#entwicklung"><b>Entwicklung</b></a>
</p>

## Auf einen Blick

- 🚀 **Ein Container, eine Datenbank.** Ein einziges Go-Binary liefert API, Share-Seiten und Dashboard. Kein Redis, kein Queue-Server.
- 📤 **Hochladen, wie du willst.** Drag & Drop, Einfügen aus der Zwischenablage, ganze Ordner, ShareX, curl und fortsetzbare tus-Uploads für große Dateien.
- 🔗 **Links mit Vorschau.** OpenGraph, Twitter-Cards, oEmbed und echte Dateiendungen, damit Links in Discord, Slack und Mastodon schön aufklappen.
- 🛡️ **Privat ab Werk.** EXIF-/GPS-Entfernung, optionale Verschlüsselung der gespeicherten Dateien, 2FA, eine strikte CSP und ein Container, der root abgibt.
- 👥 **Für kleine Gruppen gemacht.** Konten nur auf Einladung, Admin-Rolle, API-Tokens, Audit-Log und ein Papierkorb mit Wiederherstellen.
- 🎬 **Medien richtig gemacht.** Thumbnails, Video-Vorschaubilder und adaptives HLS-Streaming mit MP4-Fallback.

## Funktionen

**Hochladen**

- Upload im Dashboard per Drag & Drop (auch ganze Ordner), per Einfügen mit Strg+V
  oder über die API.
- ShareX-Unterstützung: Beim Anlegen eines API-Tokens gibt es eine fertige
  `.sxcu`-Konfiguration zum Download.
- Fortsetzbare Uploads über das [tus-1.0](https://tus.io)-Protokoll für große
  Dateien (das Dashboard nutzt tus ab 10 MiB automatisch).
- Eigene Slugs (`?slug=mein-link`) oder automatische: zufällig, aus Wörtern
  (`gfycat`) oder datumsbasiert.
- Der Dateityp wird aus dem Inhalt bestimmt (die Angabe des Clients zählt nie),
  dazu eine konfigurierbare MIME-Sperrliste und Deduplizierung per SHA-256.

**Teilen und Vorschauen**

- Share-Seiten unter `/s/{slug}` mit OpenGraph- und Twitter-Card-Tags, mehreren
  `og:image`-Einträgen, `twitter:player` für Medien und einem oEmbed-Endpunkt.
- Raw-Links tragen die echte Dateiendung (`/raw/{slug}.png`), damit Chat-Clients sie
  direkt einbetten; starke ETags und Conditional Requests.
- Bild- und Video-Thumbnails sowie Video-Metadaten (per ffmpeg, im Image enthalten).
- Adaptives HLS-Streaming für Videos, im Hintergrund transkodiert, mit MP4 als
  Rückfall.
- Pro Datei ein Löschlink im ShareX-Stil (`/d/{key}`) mit Bestätigungsseite.

**Datenschutz und Sicherheit**

- Entfernen von JPEG-Metadaten (GPS, EXIF, XMP, IPTC), standardmäßig an.
  Farbprofil und Ausrichtung bleiben erhalten. Lässt sich eine Datei nicht sauber
  bereinigen, wird sie neu kodiert oder abgelehnt, aber nie ungefiltert gespeichert.
- Optionale Verschlüsselung der gespeicherten Dateien (at rest).
- Papierkorb mit Wiederherstellung. Gelöschte Dateien werden nach einer
  einstellbaren Frist endgültig entfernt (Standard: 30 Tage); eine optionale globale
  Aufbewahrungsfrist lässt alte Dateien automatisch auslaufen.

**Konten und Verwaltung**

- Einrichtungsassistent beim ersten Aufruf im Browser: Für den ersten Admin braucht
  es keine Kommandozeile.
- Mehrere Nutzer über Einmal-Einladungscodes, mit Admin-Rolle.
- Zwei-Faktor-Authentifizierung (TOTP) mit Wiederherstellungscodes.
- Persönliche API-Tokens, Sitzungsübersicht mit Abmelden aus der Ferne,
  Datenexport (ZIP mit JSON-Manifest) und Kontolöschung.
- Admin-Einstellungen für Branding, Embeds, Upload-Limits und Fristen, dazu
  Aufrufstatistiken pro Datei und ein Audit-Log.
- Web-Push-Benachrichtigungen (optional, VAPID).
- Dashboard auf Deutsch und Englisch.

**Betrieb**

- Ein einziges Docker-Image, das read-only läuft, root-Rechte selbst abgibt und nur
  PostgreSQL braucht.
- Datenbank-Migrationen laufen beim Start automatisch.
- Einstellbarer Connection-Pool und optionale Read-Replica für leselastige Abfragen;
  Sitzungen liegen in der Datenbank, mehrere Instanzen können sich also eine
  Datenbank teilen.
- OpenAPI-3-Spezifikation mit eingebauter Swagger UI, die ohne Internetzugang
  funktioniert.

## Screenshots

<table>
  <tr>
    <td width="50%"><img src="docs/screenshots/share.webp" alt="Öffentliche Share-Seite"><br><sub><b>Share-Seite</b> — das sehen die Leute, denen du einen Link schickst, mit OpenGraph-Tags für Vorschauen.</sub></td>
    <td width="50%"><img src="docs/screenshots/lightbox.webp" alt="Lightbox-Vorschau im Dashboard"><br><sub><b>Lightbox</b> — Vollbild-Vorschau direkt aus dem Dashboard.</sub></td>
  </tr>
  <tr>
    <td><img src="docs/screenshots/profile.webp" alt="Profil mit Statistiken, Erfolgen und Upload-Heatmap"><br><sub><b>Profil</b> — Upload-Statistiken, Erfolge und eine Jahres-Heatmap.</sub></td>
    <td><img src="docs/screenshots/settings-embeds.webp" alt="Admin-Einstellungen mit Discord-Embed-Vorschau"><br><sub><b>Einstellungen</b> — Embed-Vorlagen mit Live-Vorschau für Discord; alle Laufzeit-Einstellungen liegen im Dashboard.</sub></td>
  </tr>
  <tr>
    <td><img src="docs/screenshots/traffic.webp" alt="Traffic-Statistik nach Referrer und Client"><br><sub><b>Traffic</b> — woher die Aufrufe kommen, nach Referrer und Client-Typ.</sub></td>
    <td align="center">
      <img src="docs/screenshots/mobile-dashboard.webp" alt="Dashboard auf dem Handy" width="45%">
      <img src="docs/screenshots/mobile-share.webp" alt="Share-Seite auf dem Handy" width="45%"><br>
      <sub><b>Mobil</b> — Dashboard und Share-Seiten funktionieren auch auf dem Handy.</sub>
    </td>
  </tr>
</table>

<sub>Screenshots auf Englisch; die Oberfläche gibt es auch auf Deutsch. Demo-Fotos von <a href="https://unsplash.com">Unsplash</a> über <a href="https://picsum.photos">Lorem Picsum</a>.</sub>

## Schnellstart (Docker Compose)

Voraussetzungen: Docker mit Compose-Plugin und `openssl`.

```bash
git clone https://github.com/ariazonaa/uploader.git
cd uploader
cp .env.example .env

# Zwei VERSCHIEDENE 32-Byte-Schlüssel erzeugen und in die .env eintragen:
openssl rand -hex 32   # -> UPLOADER_SESSION_HASH_KEY
openssl rand -hex 32   # -> UPLOADER_SESSION_BLOCK_KEY
# Außerdem POSTGRES_PASSWORD in der .env setzen.

docker compose up -d
```

Damit wird das fertige Image `ghcr.io/ariazonaa/uploader` samt PostgreSQL 17 gezogen.
Alle dauerhaften Daten liegen als Bind-Mount unter `./data`: `./data/files` für die
Uploads und `./data/postgres` für die Datenbank.

Danach die App im Browser öffnen. Solange noch kein Konto existiert, erscheint ein
Einrichtungsassistent, der den ersten Admin anlegt. Anschließend unter
**Einstellungen** die **öffentliche URL** setzen, damit erzeugte Links auf den
richtigen Host zeigen.

### Reverse-Proxy

Die mitgelieferte `docker-compose.yml` veröffentlicht keinen Port. Sie geht von einem
Reverse-Proxy davor aus, der TLS terminiert und an den `app`-Container auf Port 8080
weiterleitet. Der Stack hat ein eigenes Bridge-Netz mit festen Adressen; ein Proxy auf
dem Host erreicht die App standardmäßig unter `10.89.0.42:8080`. Ein nginx-Beispiel
liegt in [`deploy/nginx/example.conf`](deploy/nginx/example.conf). Kollidiert das
Standard-Subnetz mit einem vorhandenen Netz, lassen sich `UPLOADER_SUBNET`,
`UPLOADER_GATEWAY`, `UPLOADER_APP_IP` und `UPLOADER_POSTGRES_IP` in der `.env` ändern.

Der Proxy sollte `X-Forwarded-For` ergänzen (in nginx `$proxy_add_x_forwarded_for`)
und keine eigenen Security-Header setzen: Die App setzt sie selbst und lockert sie
für einzelne Routen bewusst (etwa darf `/p/{slug}` für Embeds eingebettet werden).

### Lokal ausprobieren ohne Proxy

Zum Testen auf dem eigenen Rechner das Entwicklungs-Overlay dazunehmen. Es
veröffentlicht die App unter `http://127.0.0.1:8080` und schaltet sichere Cookies und
Proxy-Vertrauen für reines HTTP ab:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d
```

Das Overlay bindet nur an 127.0.0.1. Auf einem öffentlichen Server hat es nichts
verloren.

### Nutzer per Kommandozeile anlegen

Normalerweise übernimmt das der Einrichtungsassistent, der erste Admin lässt sich aber
auch so anlegen:

```bash
docker compose exec app uploader user create --username admin --email du@example.com --password '…'
```

Weitere Nutzer kommen über Einladungscodes dazu: `UPLOADER_REGISTRATION=invite` setzen
und die Codes im Dashboard unter **Einladungen** erzeugen.

## Konfiguration

Es gibt zwei Arten von Einstellungen.

- **Infrastruktur und Geheimnisse** kommen aus Umgebungsvariablen (üblicherweise aus
  der `.env`). Die Liste steht unten.
- **Alles andere wird im Admin-Dashboard (Einstellungen) festgelegt, nie über
  Umgebungsvariablen.** Dazu gehören App-Name, öffentliche URL, Embed-Vorlagen und
  -Farbe, Branding (Logo, Text und Hintergrund der Login-Seite, „powered by"-Hinweis),
  Impressum, maximale Upload-Größe, EXIF-Entfernung, Verschlüsselungs-Standard,
  Slug-Strategie, gesperrte MIME-Typen sowie die Aufbewahrungsfristen für Dateien,
  Papierkorb und Audit-Log. Diese Werte liegen in der Datenbank, werden beim ersten
  Start mit Standardwerten befüllt und überstehen Neustarts und Updates.

| Variable | Standard | Beschreibung |
| --- | --- | --- |
| `UPLOADER_SESSION_HASH_KEY` | *Pflicht* | 64 Hex-Zeichen (`openssl rand -hex 32`). Signiert das Session-Cookie. |
| `UPLOADER_SESSION_BLOCK_KEY` | *Pflicht* | 64 Hex-Zeichen, anders als der Hash-Key. Verschlüsselt das Session-Cookie und die gespeicherten 2FA-Geheimnisse. |
| `POSTGRES_PASSWORD` | *Pflicht* | Datenbank-Passwort; der Compose-Stack nutzt es für beide Container. |
| `UPLOADER_DATABASE_URL` | von Compose gesetzt | PostgreSQL-Verbindung. Nur nötig, wenn das Binary direkt läuft. |
| `UPLOADER_DATABASE_REPLICA_URL` | *leer* | Optionale Read-Replica für leselastige Abfragen. Schreibzugriffe gehen immer an die Primärdatenbank. |
| `UPLOADER_DB_MAX_CONNS` / `UPLOADER_DB_MIN_CONNS` | `0` (intern: 12 / 2) | Größe des Connection-Pools. |
| `UPLOADER_DB_MAX_CONN_LIFETIME` / `UPLOADER_DB_MAX_CONN_IDLE_TIME` | *leer* (intern: `1h` / `30m`) | Lebensdauer der Pool-Verbindungen (Go-Durations). |
| `UPLOADER_ENCRYPTION_KEY` | *leer* | 64 Hex-Zeichen, Schlüssel für die Verschlüsselung at rest. Leer wird er aus dem Session-Block-Key abgeleitet, siehe [Schlüsselrotation](#schlüsselrotation). |
| `UPLOADER_REGISTRATION` | `off` | `off` oder `invite` (wer einen gültigen Einladungscode hat, kann sich registrieren). |
| `UPLOADER_TRUST_PROXY` | `true` in Compose, `false` im Binary | `X-Forwarded-*`-Header auswerten. Siehe [Reverse-Proxy und Client-IPs](#reverse-proxy-und-client-ips). |
| `UPLOADER_CLIENT_IP_HEADER` | *leer* | Client-IP nur aus diesem Header lesen (z. B. `X-Real-IP` oder `CF-Connecting-IP`). |
| `UPLOADER_SECURE_COOKIES` | `true` | Nur zum Testen über reines HTTP auf `false` setzen. |
| `UPLOADER_SESSION_TTL` | `720h` | Lebensdauer einer Sitzung. |
| `UPLOADER_SLUG_LENGTH` | `6` | Länge zufälliger Slugs (mindestens 4). |
| `UPLOADER_VAPID_PUBLIC_KEY` / `UPLOADER_VAPID_PRIVATE_KEY` | *leer* | Web-Push-Schlüssel, erzeugt mit `uploader vapid`. Ohne beide bleibt Push aus. |
| `UPLOADER_VAPID_SUBJECT` | `mailto:admin@example.com` | Kontakt-URI für die Push-Dienste. |
| `UPLOADER_SEED_PASSWORD` | *leer* | Passwort für `uploader user create`, wenn `--password` fehlt. |
| `UPLOADER_HTTP_ADDR` | `:8080` | Listen-Adresse (von Compose nicht durchgereicht; nur relevant, wenn das Binary direkt läuft). |
| `UPLOADER_STORAGE_FILE_ROOT` | `./data/files` (`/data/files` in Compose) | Ablageort der Uploads. |
| `UPLOADER_IMAGE` / `UPLOADER_TAG` | `ghcr.io/ariazonaa/uploader` / `latest` | Welches Image Compose zieht. Siehe [Aktualisieren](#aktualisieren-und-version-festlegen). |
| `UPLOADER_SUBNET` / `UPLOADER_GATEWAY` | `10.89.0.40/29` / `10.89.0.41` | Nur Compose: das Bridge-Netz des Stacks. |
| `UPLOADER_APP_IP` / `UPLOADER_POSTGRES_IP` | `10.89.0.42` / `10.89.0.43` | Nur Compose: feste Container-Adressen (der Proxy zielt auf die App-IP). |

Optionale Variablen, die in der `.env` leer bleiben, behalten ihren eingebauten
Standardwert.

### Reverse-Proxy und Client-IPs

Für `UPLOADER_TRUST_PROXY` gibt es keinen Wert, der immer sicher ist; er muss zur Art
des Deployments passen.

- **Hinter einem Proxy und `false`:** Jede Anfrage scheint vom Proxy zu kommen, alle
  Clients teilen sich also einen Rate-Limit-Topf. Fünf Fehl-Logins von irgendwem
  sperren das Login für alle.
- **Direkt erreichbar und `true`:** Clients können sich jede beliebige IP
  zuschreiben.

Ist die Option an, liest die App den **rechtesten** Eintrag aus `X-Forwarded-For` (den
Hop, den der eigene Proxy angehängt hat) und greift ersatzweise auf `X-Real-IP`
zurück. Setzt der Proxy nur einen der beiden Header, kommt der andere unverändert vom
Client durch. Dann `UPLOADER_CLIENT_IP_HEADER` auf genau den Header setzen, den der
Proxy schreibt.

### Schlüsselrotation

`UPLOADER_SESSION_BLOCK_KEY` verschlüsselt die gespeicherten 2FA-Geheimnisse. Ist
`UPLOADER_ENCRYPTION_KEY` leer, wird auch der Schlüssel für die Verschlüsselung at rest
daraus abgeleitet. Wer den Session-Key rotiert, macht damit verschlüsselte Dateien
unlesbar und alle 2FA-Einrichtungen ungültig. **Deshalb `UPLOADER_ENCRYPTION_KEY`
ausdrücklich setzen, bevor die Verschlüsselung produktiv eingeschaltet wird.** Für 2FA
bleibt nur die Neueinrichtung. Solange der Schlüssel abgeleitet wird, warnt der Server
beim Start.

## ShareX und API

Im Dashboard unter **Tokens** einen API-Token anlegen. Er wird nur einmal angezeigt;
im selben Dialog gibt es eine fertige `.sxcu`-Datei für ShareX.

Hochgeladen wird per Multipart-`POST` an `/api/v1/upload`, die Datei im Feld `file`.
Authentifiziert wird mit `Authorization: Bearer upl_…` (oder `X-Uploader-Token: upl_…`).

```bash
curl -F "file=@bild.png" -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/upload

# Flameshot (Linux): Screenshot direkt hochladen
flameshot gui --raw | curl -F "file=@-;filename=shot.png" \
  -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/upload
```

Die JSON-Antwort enthält unter anderem `url` (Share-Seite), `raw` (Direktlink),
`thumb` (sofern ein Thumbnail existiert) und `delete` (öffentlicher Löschlink).

Optionale Query-Parameter:

| Parameter | Wirkung |
| --- | --- |
| `slug=mein-link` | Eigener Slug (3–64 Buchstaben, Ziffern, `-` oder `_`). `409`, wenn er vergeben ist. |
| `slug_strategy=random\|gfycat\|date` | Benennungsschema für automatische Slugs. |
| `strip_exif=false` | Bild-Metadaten bei diesem Upload behalten. |
| `encrypt=true\|false` | Verschlüsselungs-Standard für diesen Upload übersteuern. |

Große Dateien können stattdessen den tus-Endpunkt unter `/api/v1/tus` nutzen.

Die gesamte API ist als OpenAPI-3-Dokument unter `/api/v1/openapi.yaml` beschrieben
(Anmeldung oder Token nötig). Eine interaktive Swagger UI gibt es im Dashboard unter
**API** (`/docs`); sie wird von der App selbst ausgeliefert, ohne externes CDN. Aus der
Spezifikation lässt sich ein typisierter Client erzeugen, zum Beispiel:

```bash
curl -H "Authorization: Bearer upl_…" https://i.example.com/api/v1/openapi.yaml -o openapi.yaml
npx @openapitools/openapi-generator-cli generate -i openapi.yaml -g typescript-fetch -o ./client
```

## Aktualisieren und Version festlegen

```bash
docker compose pull && docker compose up -d
```

Migrationen laufen beim Start automatisch. Standardmäßig zieht Compose
`ghcr.io/ariazonaa/uploader:latest`. Für den Produktivbetrieb empfiehlt es sich, in der
`.env` einen bestimmten Tag festzulegen, damit ein Update nur dann passiert, wenn man
es will:

```env
UPLOADER_TAG=<Tag oder Commit-SHA>
```

Mit `UPLOADER_IMAGE` lässt sich ein anderes Image-Repository wählen, etwa ein Mirror
oder ein eigener Build. Wer das Image lieber selbst aus dem Quellcode baut, nimmt das
Build-Overlay:

```bash
docker compose -f docker-compose.yml -f docker-compose.build.yml up -d --build
```

### Kommandozeile

Das Binary (und damit der Container) kennt einige Unterbefehle:

```text
uploader serve                                  Server starten (führt vorher Migrationen aus)
uploader migrate [up|down|status|reset]         Datenbank-Migrationen verwalten
uploader user create --username U --email E --password P   Admin anlegen
uploader user disable-2fa --username U          2FA für ausgesperrte Nutzer abschalten
uploader seed                                   Demo-Admin und Beispieldateien (Entwicklung)
uploader vapid                                  Web-Push-Schlüsselpaar erzeugen
uploader healthcheck                            /api/v1/ready prüfen (nutzt das Image)
uploader version
```

## Entwicklung

Voraussetzungen: **Go 1.27**, **Node.js 22+** und **PostgreSQL 13+** (der
`postgres`-Dienst aus der Compose-Datei reicht).

Mit zwei Terminals:

```bash
# Terminal 1: Datenbank (vom Dev-Overlay auf 127.0.0.1:5432 veröffentlicht) und Backend
docker compose -f docker-compose.yml -f docker-compose.dev.yml up -d postgres
cd backend
UPLOADER_DATABASE_URL='postgres://uploader:<POSTGRES_PASSWORD>@localhost:5432/uploader?sslmode=disable' \
UPLOADER_SESSION_HASH_KEY=<64 Hex-Zeichen> \
UPLOADER_SESSION_BLOCK_KEY=<64 andere Hex-Zeichen> \
UPLOADER_SECURE_COOKIES=false \
go run ./cmd/uploader serve

# Terminal 2: Vite-Dev-Server, leitet API und öffentliche Routen an :8080 weiter
cd frontend
npm ci
npm run dev
```

Das Datenbank-Passwort ist das `POSTGRES_PASSWORD` aus der `.env`. Feste
Session-Schlüssel verwenden (etwa die aus der `.env`); mit neuen Schlüsseln bei jedem
Start ist man jedes Mal abgemeldet. `go run ./cmd/uploader seed` legt einen Demo-Admin
mit Beispieldateien an.

### Prüfungen

Backend (in `backend/`):

```bash
gofmt -l .                                   # darf nichts ausgeben
go vet ./...
go test ./...
go build ./...
GOOS=linux GOARCH=amd64 go build ./...       # Container-Ziel; ein Teil des Codes ist Linux-spezifisch
GOOS=linux GOARCH=amd64 golangci-lint run    # Lint ebenfalls für das Container-Ziel
```

Die Integrationstests laufen gegen ein echtes PostgreSQL (13+) und stecken hinter dem
Build-Tag `integration`. `UPLOADER_TEST_DATABASE_URL` muss auf einen **Wegwerf-Server**
zeigen, dessen Rolle `CREATE DATABASE` darf. Jedes Testpaket migriert eine
Vorlagen-Datenbank und gibt jedem Test einen frischen Klon davon. Ohne die Variable
werden die Tests übersprungen.

```bash
UPLOADER_TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable' \
go test -tags integration -count=1 ./...
```

Frontend (in `frontend/`):

```bash
npm run typecheck
npm run build                                 # der Produktions-Build, der eingebettet wird
npx prettier --check "src/**/*.{ts,tsx,css}"
```

### Technik

- **Backend:** Go, `chi`, PostgreSQL über `pgx` mit handgeschriebenem SQL,
  eingebettete `goose`-Migrationen, `gorilla/securecookie`, `disintegration/imaging`.
- **Frontend:** React 19, Vite, TypeScript, Tailwind CSS v4, TanStack Query,
  `lucide-react`. Für die Produktion per `go:embed` ins Go-Binary eingebettet.
- **Medien:** ffmpeg und ffprobe als statische Binaries aus `mwader/static-ffmpeg`.
- **Speicher:** lokales Dateisystem, nach Slug in Unterverzeichnisse aufgeteilt.

## Sicherheit

Ein kurzer Überblick. Sicherheitslücken bitte wie in [SECURITY.md](SECURITY.md)
beschrieben melden, nicht in einem öffentlichen Issue.

- **Container:** Das Image startet nur als root, um die Rechte am Datenverzeichnis zu
  korrigieren, und wechselt dann im Binary selbst auf uid 10001 (kein `su-exec`, kein
  Entrypoint-Skript). Der Compose-Stack betreibt es mit read-only Root-Dateisystem,
  `no-new-privileges`, ohne Capabilities außer den fünf, die der Rechtewechsel
  braucht, sowie mit Speicher- und CPU-Limits.
- **Header:** strikte Content-Security-Policy (keine Inline- oder externen Skripte),
  `X-Content-Type-Options: nosniff`, `X-Frame-Options: DENY` und HSTS über HTTPS.
  Potenziell aktive Inhalte (HTML, SVG, XML, JavaScript) werden immer als Download
  ausgeliefert, Uploads taugen also nicht für Stored XSS.
- **Uploads:** Dateitypen werden aus dem Inhalt bestimmt, JPEG-Metadaten
  standardmäßig entfernt. Das Dekodieren von Bildern ist begrenzt (50 Megapixel,
  16384 px pro Kante, gemeinsames Speicherbudget), um Dekompressionsbomben
  abzuwehren; ffmpeg läuft mit Zeit- und Ausgabelimits.
- **Anmeldung:** Sitzungen sind verschlüsselte und signierte Cookies (`HttpOnly`,
  `Secure`, `SameSite=Lax`) mit serverseitigem Sitzungseintrag. API-Tokens werden nur
  als Hash gespeichert. TOTP-Codes sind nur einmal gültig. Login, 2FA und Uploads sind
  pro IP (IPv6 pro /64) und pro Konto rate-limitiert.
- **Web Push:** Nur Endpunkte bekannter Push-Dienste werden angenommen, Push-Abos
  führen also nicht ins interne Netz.

## Mitmachen

Beiträge sind willkommen. Vor einem Pull Request bitte [CONTRIBUTING.md](CONTRIBUTING.md)
lesen. Manche Funktionen wurden geprüft und bewusst weggelassen (etwa S3-Speicher,
Ordner und Tags oder passwortgeschützte Dateien). Größere Vorhaben deshalb am besten
zuerst in einem Issue besprechen.

## Lizenz

Uploader steht unter der [MIT-Lizenz](LICENSE).
