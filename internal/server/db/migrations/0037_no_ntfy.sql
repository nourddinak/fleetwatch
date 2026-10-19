-- ntfy is no longer a notification channel: its saved settings go.
DELETE FROM notification_channels WHERE provider = 'ntfy';
