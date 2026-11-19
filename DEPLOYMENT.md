# Deploy Wisp

## Docker Compose

1. Copy the environment template and fill in the public domain, S3/R2 credentials, and bucket URL.

   ```sh
   cp .env.example .env
   ```

2. Set `BLOG_DOMAIN` and `BLOG_BASE_URL` to the same public site. For Cloudflare R2, set `S3_ENDPOINT` to the account endpoint and `S3_REGION=auto`.

3. Start Wisp and Caddy.

   ```sh
   docker compose up -d --build
   ```

4. Open the site and complete the first-run setup wizard. The first account becomes the admin account.

Caddy obtains and renews HTTPS certificates when `BLOG_DOMAIN` points at the server and ports 80 and 443 are reachable. For local testing, use `BLOG_DOMAIN=localhost` and visit `http://localhost`.

## Data and backups

The SQLite database lives in the named `wisp-data` volume. The container is non-root and has a read-only root filesystem. Back up the database from a stopped app or with SQLite's online backup command. A plain copy of a live WAL-mode database is not safe.

Example backup from the host:

```sh
docker compose stop wisp
docker run --rm \
  -v wisp_wisp-data:/data:ro \
  -v "$PWD/backups:/backup" \
  alpine:3.20 cp /data/wisp.db /backup/wisp-$(date +%F).db
docker compose start wisp
```

Keep backups off the server. The S3/R2 image bucket is separate from the SQLite backup and needs its own retention policy.

## Checks

Wisp exposes `GET /healthz`, which returns `200 ok` only when SQLite responds. Caddy handles TLS and compression. The application adds CSP, frame, content-type, and referrer headers.
