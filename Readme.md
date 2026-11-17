# Shortly - URL Shortener

A small, login-free URL shortener written in Go with a single-page web UI. Anyone can paste a long URL, choose how long the link should live (up to 24 hours), optionally pick their own word for the short link, and share it. Expired links are removed automatically.

**At a glance**

| | |
|---|---|
| Language | Go (standard library only, no external dependencies) |
| Storage | In-memory map (no database) |
| Frontend | One `index.html` (plain HTML, CSS, JavaScript) |
| Files | 3 (`main.go`, `index.html`, `go.mod`) |
| Endpoints | 3 |
| Default port | 3000 (or the `PORT` environment variable) |

## Features

- No sign-up or login
- Expiry chosen per link: 1 to 1,440 minutes (UI offers 5 min, 30 min, 1 h, 6 h, 24 h)
- Optional custom word for the short link, otherwise an 8-character hash code
- Live countdown and one-click copy on the result card
- Expired links answer `410 Gone` and are cleaned up every minute
- Per-visitor rate limit: 10 new links per minute
- Input validation: only `http://` and `https://` URLs, maximum 2,000 characters
- Safe for many simultaneous users (map protected by a read/write mutex)
- Works on localhost and when hosted; the short link adapts to whatever address the visitor used

## Project structure

```
url-shortener/
├── main.go      # HTTP server, handlers, storage, rate limiter, cleanup loop
├── index.html   # Web UI (served at "/")
├── go.mod
└── README.md
```

## Run locally

Requirements: Go installed (`go version` to check).

```cmd
go run .
```

Open http://localhost:3000. Run the command from the project folder, because `index.html` is read from the current directory.

To use a different port:

```cmd
set PORT=8080
go run .
```

## Using the web page

1. Paste a long URL.
2. Optionally type a custom word (3 to 30 letters, numbers, `-` or `_`).
3. Pick the expiry (default 1 hour).
4. Click **Shorten link** and copy the result.

## API reference

### `POST /shorten`

Create a short link.

Request body (JSON):

| Field | Type | Required | Rules |
|---|---|---|---|
| `url` | string | yes | `http://` or `https://`, max 2,000 characters |
| `expiry_minutes` | number | yes | 1 to 1,440 |
| `alias` | string | no | 3 to 30 characters: `A-Z a-z 0-9 - _` (case-sensitive) |

Example:

```cmd
curl -X POST http://localhost:3000/shorten -H "Content-Type: application/json" -d "{\"url\":\"https://example.com/a/very/long/link\",\"expiry_minutes\":60,\"alias\":\"demo\"}"
```

Success response (`200`):

```json
{
  "short_url": "http://localhost:3000/shortly/demo",
  "expiry_date": "2026-11-17T18:30:00.123456+05:30"
}
```

Error responses (plain-text message in the body):

| Status | When |
|---|---|
| 400 | Invalid JSON, empty URL, URL longer than 2,000 characters, not a valid http(s) URL, expiry outside 1-1,440, invalid custom word |
| 405 | Method other than POST |
| 409 | Custom word already in use by an unexpired link |
| 429 | More than 10 links created by the same visitor within one minute (`Retry-After: 60` header is sent) |

### `GET /shortly/{code}`

Redirect to the original URL.

| Status | Meaning |
|---|---|
| 302 | Redirects to the original URL |
| 404 | Code does not exist |
| 410 | Link has expired (it is deleted at this point) |
| 405 | Method other than GET |

### `GET /`

Serves `index.html`. Any other unknown path returns `404`.

## Limits and configuration

All limits are constants near the top of `main.go`.

| Setting | Value | Where to change |
|---|---|---|
| Maximum expiry | 1,440 minutes (24 hours) | `maxExpiryMinutes` (and the chips in `index.html`) |
| Minimum expiry | 1 minute | validation in `shortURLHandler` |
| Maximum URL length | 2,000 characters | `maxURLLength` (and `maxlength` in `index.html`) |
| Custom word length | 3 to 30 characters | `aliasPattern` (and `pattern`/`maxlength` in `index.html`) |
| Request body size | 8 KB | `http.MaxBytesReader` in `shortURLHandler` |
| Rate limit | 10 links per minute per visitor | `rateLimit`, `rateWindow` |
| Cleanup interval | every 1 minute | `cleanupLoop` |
| Generated code length | 8 hex characters (16^8 = 4,294,967,296 possible codes) | `generateShortURL` |
| Port | `PORT` env var, default 3000 | `main` |

## How it works

```
Browser ──POST /shorten──► validate URL, expiry, alias
                           check rate limit (per IP)
                           build id = alias OR md5(url + current time)[:8]
                           store in map under a mutex
        ◄── short link ────

Browser ──GET /shortly/id─► look up id
                            not found  → 404
                            expired    → delete + 410
                            otherwise  → 302 redirect to the original URL

Every minute: delete expired links and stale rate-limit entries
```

Each link is stored as `{ID, OriginalURL, ShortURL, CreationDate, ExpiryDate}`.

## Capacity and performance

> These figures are **estimates from reading the code, not measurements.** Run the benchmark below to get real numbers on your own machine or host.

| Environment | Redirects / second | New links / second |
|---|---|---|
| Typical laptop | thousands | hundreds to 1,000+ |
| Render free tier (shared CPU) | a few hundred | tens to ~200 |

Memory: each stored link takes roughly 0.3 to 3 KB depending on URL length (about 3 KB at the 2,000-character maximum), so a 512 MB instance can hold on the order of 100,000 to 1,000,000+ live links. Expired links are removed within about a minute.

Generated-code collisions: with 8 hex characters, the chance that two live links share a code is about 1% at 10,000 live links and about 0.001% at 1,000.

Benchmark it with [`hey`](https://github.com/rakyll/hey):

```cmd
go install github.com/rakyll/hey@latest
hey -n 5000 -c 100 -m POST -T "application/json" -d "{\"url\":\"https://example.com\",\"expiry_minutes\":5}" http://localhost:3000/shorten
```

Note that the rate limiter (10 per minute per IP) will reject most of those requests from a single machine. For a raw throughput test, temporarily raise `rateLimit` in `main.go`.

## Deploy

1. Push to GitHub:
   ```cmd
   git init
   git add main.go index.html go.mod README.md
   git commit -m "URL shortener"
   git branch -M main
   git remote add origin https://github.com/mansi-wagh/url-shortener.git
   git push -u origin main
   ```
2. On [Render](https://render.com): **New + → Web Service**, select the repo, then set
   - Language: **Go**
   - Build command: `go build -o app .`
   - Start command: `./app`
   - Instance type: **Free**
3. Open the URL Render gives you. Every later `git push` to `main` redeploys automatically.

If the build fails on the Go version, add an environment variable `GO_VERSION` matching the output of `go version`.

## Known limitations

- **Data is lost on restart.** Links live in memory, and free hosting tiers sleep after about 15 minutes of inactivity.
- **Single instance only.** Each server instance has its own map, so it cannot be scaled horizontally without a shared database.
- **Rate limit can be bypassed** by faking the `X-Forwarded-For` header; it stops casual spam, not a determined attacker.
- **Rare code collisions** on generated codes currently return the "already taken" error (409) even though the user did not choose a word; retrying works.
- **Redirects are not rate limited** and no analytics (click counts) are recorded.

## future improvement

- [ ] Store links in PostgreSQL so they survive restarts and allow multiple instances
- [ ] Retry automatically when a generated code collides
- [ ] Click counter per link
- [ ] QR code for each short link
- [ ] Stricter client-IP detection behind a trusted proxy