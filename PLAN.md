# Wisp: a small self-hosted blog

One Go binary, one SQLite file, one container. Writing happens in a browser admin with a rich text editor. Readers get plain server-rendered HTML with no JavaScript at all.

The software is called Wisp. Site identity (name, description, author, language, base URL, pagination) is data in the database, edited at `/admin/settings`, not baked into the binary.

This document is the build plan. It assumes the answers from the design questions and flags the places where those answers create tension.

## Decisions

| Area | Choice | Consequence |
| --- | --- | --- |
| Runtime | Go, single static binary | No runtime on the server, fast cold start, trivial deploy |
| HTTP | stdlib `net/http` + `html/template` | No framework, roughly 200 lines of routing and middleware |
| Storage | SQLite via `modernc.org/sqlite` | Pure Go, no cgo, DB is one file on a volume |
| Rendering | Server-rendered HTML | No hydration, no client router, no JSON API for pages |
| Client JS | Zero on public pages, Tiptap in admin only | Admin pays a one-time ~150 KB gzip cost |
| Site config | `settings` table, edited at `/admin/settings` | Name, description, author, language, base URL, pagination change without a restart |
| Auth | Email + password, invite-only | First account created at `/setup`, others by invite link |
| Roles | Single role | Every account can write, publish, invite, and delete |
| Features | Tags, full-text search, drafts, scheduling, RSS, sitemap | All derived from the same `posts` table |
| Images | S3-compatible bucket, presigned upload | Browser uploads directly, app never handles bytes |
| TLS | Reverse proxy (Caddy) | App speaks plain HTTP on `:8080` |

## Not building

Comments, likes, analytics dashboards, webmentions, multi-language, plugin or theme systems, public signup, third-party API, client-side rendering, markdown import. The editor stores HTML, so there is no markdown pipeline in the read path.

## Budget

| Item | Target |
| --- | --- |
| Binary size | under 50 MB (aim 35) |
| Idle RSS | under 40 MB |
| Public page JS | 0 bytes |
| Public CSS | under 10 KB gzip, one file |
| Admin JS | ~150 KB gzip, editor pages only |
| HTML TTFB | under 20 ms on a 1 vCPU box with 1k posts |
| Direct dependencies | 6 or fewer |
| Hosting | fits on a $5 VPS |

The budget is the feature. If a change pushes three of these numbers up, the change is wrong.

## Request flow

```
reader   -> Caddy (TLS, gzip) -> app :8080 -> html/template + SQLite
writer   -> Caddy             -> app :8080 -> session + CSRF -> admin handler -> SQLite
editor   -> Tiptap in browser -> POST form -> sanitize -> SQLite
upload   -> app issues presigned POST -> browser posts file straight to S3 -> public URL inserted into post
```

Only two things leave the server: HTML and presigned upload credentials.

## Repo layout

```
cmd/blog/main.go            wiring, flags, graceful shutdown
internal/config/            env parsing, defaults, validation
internal/db/                open, pragmas, migrations, small query helpers
internal/auth/              argon2id, sessions, invites, CSRF, rate limit
internal/posts/             store, publishing rules, slugs, tags
internal/settings/          typed site settings, defaults, cache invalidation
internal/search/            FTS5 queries and fallback
internal/uploads/           S3 presign, key policy
internal/web/               handlers, middleware, templates, embedded static
migrations/001_init.sql     schema (embedded)
admin/                      Tiptap source, built by esbuild
web/static/                 CSS + built admin.js (embedded)
Dockerfile, compose.yaml, Caddyfile
```

Templates live next to the handlers and are parsed once at startup from `embed.FS`. A bad template should crash the process at boot, not at 3am on a request.

## Data model

```sql
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;
PRAGMA busy_timeout = 5000;
PRAGMA synchronous = NORMAL;

CREATE TABLE settings (
  key        TEXT PRIMARY KEY,
  value      TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE users (
  id            INTEGER PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE COLLATE NOCASE,
  name          TEXT NOT NULL,
  password_hash TEXT NOT NULL,              -- argon2id encoded string
  created_at    TEXT NOT NULL,
  last_login_at TEXT
);

CREATE TABLE sessions (
  token_hash BLOB PRIMARY KEY,              -- sha256 of a 32-byte random token
  user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf_token TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE invites (
  id          INTEGER PRIMARY KEY,
  token_hash  BLOB NOT NULL UNIQUE,
  email       TEXT,                         -- optional binding
  created_by  INTEGER REFERENCES users(id),
  expires_at  TEXT NOT NULL,
  accepted_at TEXT,
  created_at  TEXT NOT NULL
);

CREATE TABLE posts (
  id           INTEGER PRIMARY KEY,
  slug         TEXT NOT NULL UNIQUE,
  title        TEXT NOT NULL,
  summary      TEXT NOT NULL DEFAULT '',
  body_html    TEXT NOT NULL DEFAULT '',    -- sanitized
  body_text    TEXT NOT NULL DEFAULT '',    -- tags stripped, for FTS and excerpts
  tags_text    TEXT NOT NULL DEFAULT '',    -- denormalized for FTS
  cover_url    TEXT,
  status       TEXT NOT NULL DEFAULT 'draft'
               CHECK (status IN ('draft', 'published')),
  published_at TEXT,                        -- ISO-8601 UTC, may be in the future
  author_id    INTEGER REFERENCES users(id),
  created_at   TEXT NOT NULL,
  updated_at   TEXT NOT NULL
);

CREATE INDEX posts_public ON posts(status, published_at DESC);

CREATE TABLE tags (
  id   INTEGER PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL
);

CREATE TABLE post_tags (
  post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
  tag_id  INTEGER NOT NULL REFERENCES tags(id)  ON DELETE CASCADE,
  PRIMARY KEY (post_id, tag_id)
);

CREATE VIRTUAL TABLE posts_fts USING fts5(
  title, body_text, tags_text,
  content = 'posts', content_rowid = 'id',
  tokenize = 'unicode61 remove_diacritics 2'
);

-- Trigger bodies for posts_ai / posts_ad / posts_au keep posts_fts in sync.
-- See migrations/001_init.sql.
```

I use `tags_text` rather than joining in the FTS query. It is one extra column to maintain on save, and it makes ranking and snippets simple. FTS5 external-content tables need exact delete rows in triggers; getting that wrong produces phantom search results, so the migration includes a test that deletes a post and checks the index.

## Settings

Site identity lives in the `settings` table. One accessor reads typed values into a `Site` struct, cached in memory until a write invalidates it.

| Key | Default | Validation |
| --- | --- | --- |
| `site_name` | `Wisp` | 1-60 chars |
| `site_description` | empty | 200 chars max |
| `site_author` | empty | 80 chars max |
| `language` | `en` | BCP-47 shape |
| `base_url` | set at setup | absolute http(s), no trailing slash |
| `timezone` | `UTC` | IANA name, `time/tzdata` embedded |
| `posts_per_page` | `10` | 1-50 |
| `feed_size` | `20` | 1-100 |

Defaults live in one Go map, so a missing row never panics. The setup wizard writes every row, and `/admin/settings` edits them later. Changing one invalidates the cached struct, so the next request sees it. No restart, no config file, no rebuild.

`base_url` drives canonical links, feed ids, sitemap URLs, and the cookie `Secure` and `Domain` decisions, so the validator is strict. The default site name is `Wisp`, and the footer reads "Powered by Wisp" unless changed. The product name in the code stays Wisp; the site name is just a row.

## Publishing rules

The visibility rule lives in one place, `posts.PublicQuery`:

```sql
status = 'published' AND published_at IS NOT NULL AND published_at <= :now
```

- Draft: `status = 'draft'`.
- Scheduled: `status = 'published'` with `published_at` in the future. It appears on its own when the clock passes the timestamp. No cron, no worker, no background ticker.
- Unpublish: set `status = 'draft'`. The timestamp is kept so republishing can reuse it.
- Preview: `GET /admin/posts/{id}/preview` renders a draft for a logged-in user. A signed link (HMAC over post id and expiry, 1 hour) works for people without accounts.
- Slugs are unique and editable. Renaming breaks old URLs in v1; a `post_slugs` redirect table is a later addition, not a launch feature.

Every write goes through one `posts.Save` function that sanitizes HTML, recomputes `body_text`, rebuilds `tags_text`, and touches `updated_at`. There is no second path into the table.

## Search

Index title, body text, and tag names. Rank title matches highest.

```sql
SELECT p.id, p.slug, p.title, p.summary,
       snippet(posts_fts, 1, '<mark>', '</mark>', '...', 24) AS snippet
FROM posts_fts
JOIN posts p ON p.id = posts_fts.rowid
WHERE posts_fts MATCH :query
  AND p.status = 'published'
  AND p.published_at <= :now
ORDER BY bm25(posts_fts, 10.0, 1.0, 2.0), p.published_at DESC
LIMIT 20;
```

User input is turned into a quoted FTS5 query, not passed through raw. Prefix matching on the last term gives useful results while typing without needing an autocomplete endpoint.

Fallback if FTS5 is unavailable: `LIKE '%term%'` over `title` and `body_text`, ranked in Go. At a few hundred posts this is fine, so the search page can ship even if the FTS5 spike fails.

## Routes

| Method | Path | Auth | Notes |
| --- | --- | --- | --- |
| GET | `/` | public | paginated index, 10 per page |
| GET | `/posts/{slug}` | public | 404 unless public rule passes |
| GET | `/tags` | public | tag cloud or plain list |
| GET | `/tags/{slug}` | public | paginated posts for tag |
| GET | `/search` | public | `?q=`, server-rendered results |
| GET | `/feed.xml` | public | Atom, newest 20 |
| GET | `/sitemap.xml` | public | posts, tags, index |
| GET | `/robots.txt` | public | |
| GET | `/static/*` | public | embedded, hashed, long cache |
| GET | `/healthz` | public | checks DB with `SELECT 1` |
| GET/POST | `/setup` | first run | only while `users` is empty; collects site identity and first account |
| GET/POST | `/login` | public | rate limited |
| POST | `/logout` | session | |
| GET/POST | `/invite/{token}` | public | accept invite, set password |
| GET | `/admin` | session | post list, filters by status |
| GET/POST | `/admin/posts/new` | session | editor |
| GET/POST | `/admin/posts/{id}/edit` | session | editor |
| POST | `/admin/posts/{id}/publish` | session | |
| POST | `/admin/posts/{id}/unpublish` | session | |
| POST | `/admin/posts/{id}/delete` | session | |
| GET | `/admin/posts/{id}/preview` | session | draft preview |
| GET | `/preview/{id}?token=` | signed | shareable preview |
| POST | `/admin/uploads/presign` | session + CSRF | JSON response |
| GET | `/admin/users` | session | list, invite form |
| POST | `/admin/invites` | session | create link |
| GET/POST | `/admin/settings` | session | site name, description, author, language, base URL, timezone, pagination |
| GET/POST | `/admin/profile` | session | name, password change |

Forms are plain HTML posts with a hidden `_csrf` field. The only JSON endpoint is presign. That keeps the client surface small and means the editor works without a REST layer.

## Auth and security

- Passwords hashed with argon2id, 64 MiB memory, 3 iterations, 2 threads. Minimum 12 characters, no composition rules. Compare in constant time.
- Session token is 32 random bytes. Store only its SHA-256. Cookie is `HttpOnly`, `Secure` when the base URL is https, `SameSite=Lax`, 14-day expiry, refreshed on use.
- CSRF token per session, hidden field on every form, compared with `subtle.ConstantTimeCompare`.
- Login limit of 5 attempts per 15 minutes per IP plus email, in memory. One instance, so a map with a cleanup goroutine is enough.
- `/setup` renders only when `users` is empty and 404s afterward. This avoids a shipped default password.
- Invites are single use, 7-day expiry, 32-byte token stored as SHA-256. Email binding optional.
- Sanitize on write with `bluemonday` using an explicit allowlist: `p`, `h2`-`h4`, `ul/ol/li`, `a`, `strong/em`, `code/pre`, `blockquote`, `img`, `figure/figcaption`, `hr`, `br`, `table` family. Links restricted to http, https, mailto, with `rel="noopener noreferrer"`. Images restricted to https on the configured bucket host and CDN host. No `style`, `script`, `iframe`, `form`, `svg`, `object`.
- A `blog sanitize` subcommand re-runs the current policy over stored rows, so tightening the allowlist fixes historical posts.
- Response headers: CSP `default-src 'self'; img-src 'self' https://cdn.example.com; script-src 'self'; frame-ancestors 'none'; base-uri 'none'`, plus `X-Content-Type-Options: nosniff` and `Referrer-Policy: strict-origin-when-cross-origin`.
- Because HTML is sanitized at write time, templates render it with `template.HTML`. That is the one deliberate trust boundary in the app; treat every change to the policy as a security change.

## Rendering and caching

Start with no page cache. SQLite point reads are microseconds and a blog's traffic is small. Measure at M6; only add caching if TTFB misses the budget.

- Templates parsed once from `embed.FS`, crash on error.
- `ETag` from a hash of the rendered body plus `updated_at`, so `If-None-Match` returns 304 without rendering.
- HTML: `Cache-Control: public, max-age=0, must-revalidate`.
- Static: filename hashed at build (`app.<hash>.css`), `Cache-Control: public, max-age=31536000, immutable`.
- Admin responses: `Cache-Control: no-store`.
- Caddy handles gzip and brotli. A tiny stdlib gzip middleware makes direct `go run` testing honest.

## Images on S3

`POST /admin/uploads/presign` takes `{filename, content_type}`, validates the type against `image/jpeg|png|webp|gif|avif`, and returns a presigned POST:

- Key: `uploads/{yyyy}/{mm}/{uuid}.{ext}`. Never the user's filename.
- Policy conditions: `content-length-range 0..10 MB`, `Content-Type` starts with `image/`, key prefix fixed.
- Expiry: 5 minutes.
- `Cache-Control: public, max-age=31536000, immutable` set through a form field.

The browser posts `FormData` to the bucket, then inserts `S3_PUBLIC_BASE_URL + "/" + key` into the editor. No bytes touch the app, which keeps memory and CPU flat regardless of image size.

AWS S3 and Cloudflare R2 use the same S3 adapter and presigning flow. AWS leaves `S3_ENDPOINT` empty. R2 sets it to `https://<account-id>.r2.cloudflarestorage.com` and uses the account access key and secret. `S3_PUBLIC_BASE_URL` remains separate, so a custom domain or R2 public URL does not affect upload signing.

Tradeoffs worth naming: direct upload means the server cannot strip EXIF or verify the file is really an image. The type and size caps limit the damage, and serving images from a separate host keeps any stored XSS out of the blog origin. If EXIF privacy matters, switch to proxy upload; it costs bandwidth but buys inspection.

Bucket needs CORS allowing `POST` from the blog origin and `Content-Type` in `AllowedHeaders`. No public list permission, only object read. SVG is rejected on purpose.

## Docker and deployment

```dockerfile
FROM node:22-alpine AS assets
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY admin ./admin
RUN npx esbuild admin/main.ts --bundle --minify --format=esm \
    --outfile=/out/admin.js

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=assets /out/admin.js web/static/admin.js
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" \
    -tags sqlite_fts5 -o /blog ./cmd/blog

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /blog /blog
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/blog"]
```

Infrastructure config comes from env, validated at boot. Site identity does not.

| Variable | Example | Notes |
| --- | --- | --- |
| `BLOG_LISTEN` | `:8080` | |
| `BLOG_DATA_DIR` | `/data` | holds `blog.db` and nothing else |
| `S3_BUCKET`, `S3_REGION` | | |
| `S3_ENDPOINT` | | empty for AWS, set for R2/MinIO |
| `S3_ACCESS_KEY_ID`, `S3_SECRET_ACCESS_KEY` | | or use an instance role |
| `S3_PUBLIC_BASE_URL` | `https://cdn.example.com` | |
| `S3_PATH_STYLE` | `false` | true for MinIO |
| `S3_MAX_UPLOAD_MB` | `10` | also enforced in the policy |
| `BLOG_TRUST_PROXY` | `true` | honor `X-Forwarded-For` / `Proto` from Caddy |

Site name, description, author, language, base URL, timezone, and pagination are rows in `settings`, edited at `/admin/settings`. Optionally, `BLOG_SITE_NAME`, `BLOG_SITE_DESCRIPTION`, `BLOG_AUTHOR`, `BLOG_BASE_URL`, and `BLOG_POSTS_PER_PAGE` may prefill the setup form on first run for scripted installs. After setup the database wins and the env values are ignored.

Migrations run in a transaction at startup, tracked by a `schema_version` table. Two replicas would race, so document one instance. That is already the deployment shape.

`compose.yaml` runs `app` and `caddy`, with `./data` mounted at `/data` and Caddy owning 80/443. Healthcheck hits `/healthz`. Container runs as nonroot, read-only root filesystem except `/data` and `/tmp`.

Backups: nightly `sqlite3 /data/blog.db "VACUUM INTO '/backup/blog-$(date +%F).db'"` in a sidecar or host cron, then copy offsite. WAL mode means a plain `cp` of a live DB is not safe. Litestream is the upgrade if point-in-time recovery is needed.

## Milestones

Each milestone ends in something runnable, not a layer of scaffolding.

**M0, spike, half a day.** Go module, embedded template, SQLite open with pragmas, create the FTS5 table, insert and query one row with `CGO_ENABLED=0`. Confirms `modernc.org/sqlite` plus the `sqlite_fts5` build tag works in the distroless image. If FTS5 fails here, switch to the LIKE fallback and move on.

**M1, public read path, 1.5 days.** Layout template, CSS, index with pagination, post page, tag pages, 404. Seeded posts via migration or a `blog seed` command. Accept: `curl` returns full HTML, no script tags, 304 on repeat request.

**M2, auth and settings, 1 day.** Setup wizard that collects base URL, site name, and the first account, plus login, logout, sessions, CSRF, invites, rate limit, and `/admin/settings`. Accept: fresh DB forces `/setup`; changing the site name on the settings page updates the header and feed title without a restart; a second visit to `/setup` 404s; wrong password 5 times locks the form; invite link works once.

**M3, editor and CRUD, 2.5 days.** Tiptap page, toolbar (headings, bold, lists, quote, code, link, image), title/slug/summary/tags, save as draft, publish, schedule, unpublish, delete, preview. Accept: create a post with a `<script>` payload and confirm it is gone from the saved HTML and the rendered page. Scheduled post is invisible until its timestamp.

**M4, uploads, 1 day.** Presign endpoint, editor image button, bucket CORS, alt text field. Accept: upload a 5 MB PNG in the editor, image renders from the CDN, an 11 MB file is rejected by the policy, an `.svg` is rejected by the app.

**M5, search and feeds, 1 day.** FTS query and results page, tag filter, Atom feed, sitemap, robots. Accept: search finds a phrase in body text with a highlighted snippet; feed validates; sitemap contains only public posts.

**M6, packaging and hardening, 1 day.** Dockerfile, compose, Caddyfile, migrations at boot, backups, healthcheck, headers, `blog sanitize`. Accept: `docker compose up` on a clean host serves the blog over TLS with a volume-backed DB, and deleting the container loses nothing.

Rough total: 8 to 9 focused days. M3 is the long pole because of the editor and sanitizer.

## Risks

**HTML sanitization is the main attack surface.** The editor produces HTML and templates render it unescaped. Mitigate with an allowlist policy, a test table of known XSS payloads (`<img onerror>`, `javascript:` links, nested encoded tags, `svg/onload`), sanitizing on write, and the re-sanitize command. This deserves more tests than any other package.

**FTS5 may need a build tag.** `modernc.org/sqlite` is pure Go but its extension set depends on build tags. M0 settles it. The fallback is honest: at this scale, `LIKE` search over a few hundred posts is fast enough.

**Presigned POST support varies across S3 clones.** AWS, R2, and MinIO all support it, but policy condition handling differs at the edges. Test against the real provider before M4 ships, not after.

**Tiptap breaks the "few KB" rule, but only in admin.** Public pages stay at zero JS. The editor bundle is cached for a year and only writers download it. If that is unacceptable, swap to a markdown textarea, which removes the sanitizer and the bundle but loses the WYSIWYG you asked for.

**Single role means every account is an admin.** Anyone invited can delete posts and invite others. That is what was chosen. If it stops fitting, add `users.is_admin` and one permission check in middleware; the schema already has room.

**Direct upload skips server-side inspection.** No EXIF stripping, no magic-byte check. Caps on type and size plus a separate asset host contain the risk. Proxy upload is the fix if it matters.

## Open items

Need before M1:

- Whether shareable draft preview links are required at launch, or admin-only preview is enough.
- Which S3 provider (AWS, Cloudflare R2, MinIO) so endpoint and path-style defaults are right.

Assumptions unless corrected:

- Defaults of 10 posts per page and 20 feed entries, both editable in settings.
- The setup wizard asks for base URL and site name, so nothing site-specific ships in the image.
- Timestamps stored in UTC and rendered in one fixed site timezone.
- Dark mode follows `prefers-color-scheme`; no toggle, because that would need JS.
- No comments for v1.
- One app instance behind Caddy on the same host.
