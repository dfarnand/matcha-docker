const CACHE_NAME = 'matcha-v1';
const MARKDOWN_CACHE = 'matcha-markdown-v1';

const STATIC_ASSETS = [
  '/',
  '/static/style.css',
  '/static/manifest.json',
  '/static/icons/icon-192.svg',
  '/static/icons/icon-512.svg',
  '/static/sw.js'
];

const MARKDOWN_REGEX = /\.(md)$/;

// Install: cache static assets
self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => {
      return cache.addAll(STATIC_ASSETS);
    })
  );
  self.skipWaiting();
});

// Activate: clean up old caches
self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((cacheNames) => {
      return Promise.all(
        cacheNames
          .filter((name) => name !== CACHE_NAME && name !== MARKDOWN_CACHE)
          .map((name) => caches.delete(name))
      );
    })
  );
  self.clients.claim();
});

// Fetch: stale-while-revalidate for markdown, cache-first for static
self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);

  // Handle markdown files
  if (MARKDOWN_REGEX.test(url.pathname) || url.pathname === '/files') {
    event.respondWith(staleWhileRevalidate(MARKDOWN_CACHE, event.request));
    return;
  }

  // Handle static assets
  if (url.pathname.startsWith('/static/')) {
    event.respondWith(cacheFirst(CACHE_NAME, event.request));
    return;
  }

  // Handle navigation requests
  if (event.request.mode === 'navigate') {
    event.respondWith(networkFirst(CACHE_NAME, event.request));
    return;
  }
});

// Cache-first strategy
async function cacheFirst(cacheName, request) {
  const cache = await caches.open(cacheName);
  const cachedResponse = await cache.match(request);

  if (cachedResponse) {
    return cachedResponse;
  }

  try {
    const networkResponse = await fetch(request);
    if (networkResponse.ok) {
      cache.put(request, networkResponse.clone());
    }
    return networkResponse;
  } catch (error) {
    return new Response('Offline', { status: 503 });
  }
}

// Network-first strategy
async function networkFirst(cacheName, request) {
  const cache = await caches.open(cacheName);

  try {
    const networkResponse = await fetch(request);
    if (networkResponse.ok) {
      cache.put(request, networkResponse.clone());
    }
    return networkResponse;
  } catch (error) {
    const cachedResponse = await cache.match(request);
    if (cachedResponse) {
      return cachedResponse;
    }
    return cache.match('/');
  }
}

// Stale-while-revalidate for markdown
async function staleWhileRevalidate(cacheName, request) {
  const cache = await caches.open(cacheName);
  const cachedResponse = await cache.match(request);

  const fetchPromise = fetch(request).then((networkResponse) => {
    if (networkResponse.ok) {
      cache.put(request, networkResponse.clone());
    }
    return networkResponse;
  }).catch(() => {
    return cachedResponse || new Response('No cached content', { status: 404 });
  });

  return cachedResponse || fetchPromise;
}
