CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY,
    email TEXT NOT NULL UNIQUE COLLATE NOCASE,
    name TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at TEXT NOT NULL,
    last_login_at TEXT
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash BLOB PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf_token TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS invites (
    id INTEGER PRIMARY KEY,
    token_hash BLOB NOT NULL UNIQUE,
    email TEXT,
    created_by INTEGER REFERENCES users(id),
    expires_at TEXT NOT NULL,
    accepted_at TEXT,
    created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS posts (
    id INTEGER PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    summary TEXT NOT NULL DEFAULT '',
    body_html TEXT NOT NULL DEFAULT '',
    body_text TEXT NOT NULL DEFAULT '',
    tags_text TEXT NOT NULL DEFAULT '',
    cover_url TEXT,
    status TEXT NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    published_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS posts_public ON posts(status, published_at DESC);

CREATE TABLE IF NOT EXISTS tags (
    id INTEGER PRIMARY KEY,
    slug TEXT NOT NULL UNIQUE,
    name TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS post_tags (
    post_id INTEGER NOT NULL REFERENCES posts(id) ON DELETE CASCADE,
    tag_id INTEGER NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    PRIMARY KEY (post_id, tag_id)
);

CREATE VIRTUAL TABLE IF NOT EXISTS posts_fts USING fts5(
    title, body_text, tags_text,
    content='posts', content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS posts_ai AFTER INSERT ON posts BEGIN
    INSERT INTO posts_fts(rowid, title, body_text, tags_text)
    VALUES (new.id, new.title, new.body_text, new.tags_text);
END;

CREATE TRIGGER IF NOT EXISTS posts_ad AFTER DELETE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, title, body_text, tags_text)
    VALUES ('delete', old.id, old.title, old.body_text, old.tags_text);
END;

CREATE TRIGGER IF NOT EXISTS posts_au AFTER UPDATE ON posts BEGIN
    INSERT INTO posts_fts(posts_fts, rowid, title, body_text, tags_text)
    VALUES ('delete', old.id, old.title, old.body_text, old.tags_text);
    INSERT INTO posts_fts(rowid, title, body_text, tags_text)
    VALUES (new.id, new.title, new.body_text, new.tags_text);
END;

INSERT OR IGNORE INTO settings(key, value, updated_at) VALUES
    ('site_name', 'Wisp', datetime('now')),
    ('site_description', 'A small, fast blog.', datetime('now')),
    ('site_author', '', datetime('now')),
    ('language', 'en', datetime('now')),
    ('base_url', 'http://localhost:8080', datetime('now')),
    ('timezone', 'UTC', datetime('now')),
    ('posts_per_page', '10', datetime('now')),
    ('feed_size', '20', datetime('now'));

INSERT OR IGNORE INTO tags(slug, name) VALUES ('wisp', 'Wisp'), ('go', 'Go');

INSERT OR IGNORE INTO posts(slug, title, summary, body_html, body_text, tags_text, status, published_at, created_at, updated_at)
VALUES (
    'hello-from-wisp',
    'Hello from Wisp',
    'The first post from this small publishing system.',
    '<p>Wisp is a small blog system built around plain HTML, a single Go binary, and SQLite.</p>',
    'Wisp is a small blog system built around plain HTML, a single Go binary, and SQLite.',
    'Wisp Go',
    'published',
    datetime('now'), datetime('now'), datetime('now')
);

INSERT OR IGNORE INTO post_tags(post_id, tag_id)
SELECT p.id, t.id FROM posts p, tags t
WHERE p.slug = 'hello-from-wisp' AND t.slug IN ('wisp', 'go');

INSERT OR IGNORE INTO schema_version(version, applied_at) VALUES (1, datetime('now'));
