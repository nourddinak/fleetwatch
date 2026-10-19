// Browser push was removed from Fleetwatch: this worker only unregisters itself from
// browsers that installed it before.
self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.registration.unregister()));
