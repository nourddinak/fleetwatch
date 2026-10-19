-- Browser push is gone (the phone app, WhatsApp and the other channels remain).
DROP TABLE IF EXISTS push_subscriptions;
DELETE FROM notification_channels WHERE provider = 'push';
