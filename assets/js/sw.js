// ContaBase service worker — installability only.
//
// The product deliberately does not cache authenticated HTML, HTMX fragments
// or financial responses. This worker keeps a fetch handler so browsers can
// consider the app installable, while every request continues to use the
// browser's normal network path.
self.addEventListener('install', function () {
    self.skipWaiting();
});

self.addEventListener('activate', function (event) {
    event.waitUntil(self.clients.claim());
});

self.addEventListener('fetch', function () {
    // Intentionally empty: no cache, no offline copy, no request rewriting.
});
