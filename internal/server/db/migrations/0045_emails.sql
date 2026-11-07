-- 0045_emails.sql: mail on your own domains (Emails page).
-- Raw messages live as encrypted files under OPS_DATA_DIR/mail; these tables hold
-- metadata, relationships and the search index.

CREATE TABLE email_domains (
    id             TEXT    PRIMARY KEY,
    name           TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    zone_id        TEXT    NOT NULL DEFAULT '',
    catch_all      INTEGER NOT NULL DEFAULT 0,
    catch_all_to   TEXT    NOT NULL DEFAULT '',     -- address id whose inbox gets unknown recipients
    sender_id      TEXT    NOT NULL DEFAULT '',     -- Integrations → Email sender used to send ('' = matching or default)
    forward_to     TEXT    NOT NULL DEFAULT '[]',   -- JSON: Gmail copies for the domain's addresses
    spam_threshold REAL    NOT NULL DEFAULT 5,
    checks         TEXT    NOT NULL DEFAULT '{}',   -- JSON: last DNS / routing check
    checked_at     INTEGER NOT NULL DEFAULT 0,
    created_at     INTEGER NOT NULL
);

CREATE TABLE email_addresses (
    id           TEXT    PRIMARY KEY,
    domain_id    TEXT    NOT NULL REFERENCES email_domains(id),
    local        TEXT    NOT NULL COLLATE NOCASE,
    address      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name TEXT    NOT NULL DEFAULT '',
    inbox_id     TEXT    REFERENCES email_addresses(id), -- alias: mail goes to this address's inbox (NULL = its own)
    receive      INTEGER NOT NULL DEFAULT 1,
    send         INTEGER NOT NULL DEFAULT 1,
    disabled     INTEGER NOT NULL DEFAULT 0,
    forward_mode TEXT    NOT NULL DEFAULT 'domain',      -- domain | custom | off
    forward_to   TEXT    NOT NULL DEFAULT '[]',
    notify       INTEGER NOT NULL DEFAULT 1,
    signature    TEXT    NOT NULL DEFAULT '',            -- HTML
    created_at   INTEGER NOT NULL
);
CREATE INDEX idx_email_addresses_domain ON email_addresses(domain_id);

CREATE TABLE email_threads (
    id         INTEGER PRIMARY KEY,
    mailbox_id TEXT    NOT NULL,
    subject    TEXT    NOT NULL DEFAULT '',  -- normalised, for the fallback match
    last_at    INTEGER NOT NULL
);
CREATE INDEX idx_email_threads_subject ON email_threads(mailbox_id, subject, last_at);

CREATE TABLE email_messages (
    id          INTEGER PRIMARY KEY,
    mailbox_id  TEXT    NOT NULL REFERENCES email_addresses(id),
    domain_id   TEXT    NOT NULL,
    thread_id   INTEGER NOT NULL REFERENCES email_threads(id),
    direction   TEXT    NOT NULL,              -- in | out
    folder      TEXT    NOT NULL,              -- inbox sent archive spam trash | f:<folder id>
    prev_folder TEXT    NOT NULL DEFAULT '',   -- where "not spam" / restore puts it back
    message_id  TEXT    NOT NULL,
    in_reply_to TEXT    NOT NULL DEFAULT '',
    refs        TEXT    NOT NULL DEFAULT '',
    rcpt        TEXT    NOT NULL DEFAULT '',   -- the address it arrived at (in) or was sent from (out)
    from_addr   TEXT    NOT NULL,
    from_name   TEXT    NOT NULL DEFAULT '',
    to_json     TEXT    NOT NULL DEFAULT '[]',
    cc_json     TEXT    NOT NULL DEFAULT '[]',
    bcc_json    TEXT    NOT NULL DEFAULT '[]',
    reply_to    TEXT    NOT NULL DEFAULT '',
    subject     TEXT    NOT NULL DEFAULT '',
    snippet     TEXT    NOT NULL DEFAULT '',
    size        INTEGER NOT NULL,
    attachments INTEGER NOT NULL DEFAULT 0,
    unread      INTEGER NOT NULL DEFAULT 0,
    starred     INTEGER NOT NULL DEFAULT 0,
    spam_score  REAL    NOT NULL DEFAULT 0,
    spam_why    TEXT    NOT NULL DEFAULT '[]',
    auth        TEXT    NOT NULL DEFAULT '{}', -- JSON: spf, dkim, dmarc results as reported
    sha256      TEXT    NOT NULL,
    raw_path    TEXT    NOT NULL,              -- relative to the mail directory
    sent_at     INTEGER NOT NULL,              -- the Date header (or now)
    received_at INTEGER NOT NULL,
    moved_at    INTEGER NOT NULL,              -- for retention: when it entered its folder
    UNIQUE (mailbox_id, sha256)
);
CREATE INDEX idx_email_messages_folder  ON email_messages(folder, received_at DESC);
CREATE INDEX idx_email_messages_mailbox ON email_messages(mailbox_id, folder, received_at DESC);
CREATE INDEX idx_email_messages_domain  ON email_messages(domain_id, received_at DESC);
CREATE INDEX idx_email_messages_thread  ON email_messages(thread_id, received_at);
CREATE INDEX idx_email_messages_msgid   ON email_messages(mailbox_id, message_id);
CREATE INDEX idx_email_messages_unread  ON email_messages(mailbox_id, unread) WHERE unread = 1;
CREATE INDEX idx_email_messages_starred ON email_messages(starred) WHERE starred = 1;

CREATE TABLE email_attachments (
    message_id INTEGER NOT NULL REFERENCES email_messages(id) ON DELETE CASCADE,
    part       INTEGER NOT NULL,   -- index among the message's attachments
    filename   TEXT    NOT NULL,
    type       TEXT    NOT NULL,
    size       INTEGER NOT NULL,
    content_id TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (message_id, part)
);

-- Full-text search: rowid = email_messages.id.
CREATE VIRTUAL TABLE email_fts USING fts5(subject, people, body, files, tokenize = 'unicode61 remove_diacritics 2');

CREATE TABLE email_folders (
    id         TEXT    PRIMARY KEY,
    kind       TEXT    NOT NULL,            -- folder | label
    name       TEXT    NOT NULL,
    color      TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    UNIQUE (kind, name)
);

CREATE TABLE email_message_labels (
    message_id INTEGER NOT NULL REFERENCES email_messages(id) ON DELETE CASCADE,
    label_id   TEXT    NOT NULL REFERENCES email_folders(id) ON DELETE CASCADE,
    PRIMARY KEY (message_id, label_id)
);
CREATE INDEX idx_email_message_labels_label ON email_message_labels(label_id);

CREATE TABLE email_drafts (
    id          TEXT    PRIMARY KEY,
    from_id     TEXT    NOT NULL REFERENCES email_addresses(id),
    to_json     TEXT    NOT NULL DEFAULT '[]',
    cc_json     TEXT    NOT NULL DEFAULT '[]',
    bcc_json    TEXT    NOT NULL DEFAULT '[]',
    subject     TEXT    NOT NULL DEFAULT '',
    html        TEXT    NOT NULL DEFAULT '',
    reply_to_id INTEGER,                    -- the message this answers (threading headers)
    forward_id  INTEGER,                    -- the message this forwards (its attachments come along)
    files       TEXT    NOT NULL DEFAULT '[]', -- JSON: uploaded attachments
    queued      INTEGER NOT NULL DEFAULT 0, -- waiting in the outbox: hidden from Drafts
    created_by  TEXT    NOT NULL DEFAULT '',
    updated_at  INTEGER NOT NULL
);

CREATE TABLE email_outbox (
    id          TEXT    PRIMARY KEY,
    message_id  INTEGER NOT NULL REFERENCES email_messages(id),
    draft_id    TEXT    NOT NULL DEFAULT '',
    sender_id   TEXT    NOT NULL DEFAULT '',
    from_addr   TEXT    NOT NULL,
    rcpts       TEXT    NOT NULL,           -- JSON envelope recipients (to + cc + bcc)
    state       TEXT    NOT NULL,           -- scheduled | sending | accepted | failed | unknown
    attempts    INTEGER NOT NULL DEFAULT 0,
    send_at     INTEGER NOT NULL,
    next_at     INTEGER NOT NULL,
    last_error  TEXT    NOT NULL DEFAULT '',
    accepted_at INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL
);
CREATE INDEX idx_email_outbox_due ON email_outbox(state, next_at);

CREATE TABLE email_contacts (
    id        INTEGER PRIMARY KEY,
    address   TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    name      TEXT    NOT NULL DEFAULT '',
    company   TEXT    NOT NULL DEFAULT '',
    notes     TEXT    NOT NULL DEFAULT '',
    uses      INTEGER NOT NULL DEFAULT 0,
    last_used INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE email_spam_rules (
    id         TEXT    PRIMARY KEY,
    scope      TEXT    NOT NULL DEFAULT '', -- '' (everything) | domain id | address id
    pattern    TEXT    NOT NULL COLLATE NOCASE, -- someone@example.com or @example.com
    action     TEXT    NOT NULL,            -- allow | block
    created_at INTEGER NOT NULL,
    UNIQUE (scope, pattern)
);

CREATE TABLE email_grants (
    member_id  TEXT NOT NULL REFERENCES members(id) ON DELETE CASCADE,
    scope_kind TEXT NOT NULL,   -- domain | address
    scope_id   TEXT NOT NULL,
    level      TEXT NOT NULL,   -- read | send | manage
    PRIMARY KEY (member_id, scope_kind, scope_id)
);

-- Inbound posts the Worker could not hand over cleanly (parse errors etc.): kept for a retry.
CREATE TABLE email_inbound_failures (
    id          INTEGER PRIMARY KEY,
    rcpt        TEXT    NOT NULL,
    mail_from   TEXT    NOT NULL,
    size        INTEGER NOT NULL,
    raw_path    TEXT    NOT NULL DEFAULT '',
    error       TEXT    NOT NULL,
    received_at INTEGER NOT NULL
);
