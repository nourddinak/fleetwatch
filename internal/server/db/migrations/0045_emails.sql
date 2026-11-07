-- 0045_emails.sql: Custom domain email system

CREATE TABLE IF NOT EXISTS email_domains (
    id TEXT PRIMARY KEY,
    domain TEXT NOT NULL UNIQUE,
    cloudflare_zone_id TEXT,
    inbound_mode TEXT NOT NULL DEFAULT 'worker', -- 'worker' | 'cloudflare_forward' | 'both'
    forward_email TEXT,                          -- e.g. user@gmail.com
    outbound_provider TEXT NOT NULL DEFAULT 'resend', -- 'resend' | 'smtp'
    resend_domain_id TEXT,
    resend_api_key TEXT,
    smtp_config TEXT,                            -- JSON encrypted credentials if custom SMTP
    dkim_status TEXT NOT NULL DEFAULT 'pending',
    spf_status TEXT NOT NULL DEFAULT 'pending',
    mx_status TEXT NOT NULL DEFAULT 'pending',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS email_aliases (
    id TEXT PRIMARY KEY,
    domain_id TEXT NOT NULL REFERENCES email_domains(id) ON DELETE CASCADE,
    local_part TEXT NOT NULL,                    -- 'contact', 'noreply', or '*' for catch-all
    full_address TEXT NOT NULL UNIQUE,           -- 'contact@example.com'
    display_name TEXT,                           -- e.g. 'Support Team'
    delivery_mode TEXT NOT NULL DEFAULT 'inbox', -- 'inbox' | 'forward' | 'both'
    forward_to TEXT,                             -- destination email (e.g. user@gmail.com)
    is_active INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS email_messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL,
    domain_id TEXT NOT NULL REFERENCES email_domains(id) ON DELETE CASCADE,
    alias_id TEXT REFERENCES email_aliases(id) ON DELETE SET NULL,
    direction TEXT NOT NULL,                     -- 'inbound' | 'outbound'
    folder TEXT NOT NULL DEFAULT 'inbox',        -- 'inbox' | 'sent' | 'trash'
    from_address TEXT NOT NULL,
    from_name TEXT,
    to_addresses TEXT NOT NULL,                  -- JSON array of strings
    cc_addresses TEXT,                           -- JSON array of strings
    bcc_addresses TEXT,                          -- JSON array of strings
    reply_to TEXT,
    subject TEXT NOT NULL,
    snippet TEXT,
    body_text TEXT,
    body_html TEXT,
    message_id TEXT,
    in_reply_to TEXT,
    references_header TEXT,
    has_attachments INTEGER NOT NULL DEFAULT 0,
    is_read INTEGER NOT NULL DEFAULT 0,
    is_starred INTEGER NOT NULL DEFAULT 0,
    received_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_email_messages_thread ON email_messages(thread_id);
CREATE INDEX IF NOT EXISTS idx_email_messages_folder ON email_messages(folder, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_email_messages_domain ON email_messages(domain_id, received_at DESC);
CREATE INDEX IF NOT EXISTS idx_email_messages_alias ON email_aliases(domain_id);

CREATE TABLE IF NOT EXISTS email_attachments (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL REFERENCES email_messages(id) ON DELETE CASCADE,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    size_bytes INTEGER NOT NULL,
    storage_path TEXT NOT NULL
);
