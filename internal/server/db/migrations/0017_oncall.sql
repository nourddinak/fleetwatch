-- Whose phone it is: '' = the owner. Team members register their own phones for on-call alerts.
ALTER TABLE push_subscriptions ADD COLUMN member_id TEXT NOT NULL DEFAULT '';
