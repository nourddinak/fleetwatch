-- The browsers each account signed in with ("Chrome on Windows"): a sign-in from
-- another one is told to its person by a WhatsApp call (cmd/server/signins.go).
CREATE TABLE known_browsers (
    person   TEXT    NOT NULL,
    browser  TEXT    NOT NULL,
    first_at INTEGER NOT NULL,
    last_at  INTEGER NOT NULL,
    PRIMARY KEY (person, browser)
);
