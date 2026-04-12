// Service Worker — Offline Data Sync Engine
// Handles: Background Sync, Push Notifications, offline fallback.

const VERSION = 'app-sw-v1';

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

// Offline fallback for navigation requests
self.addEventListener('fetch', (event) => {
  if (event.request.mode !== 'navigate') return;
  event.respondWith(
    fetch(event.request).catch(() =>
      new Response('<html><body style="font-family:system-ui;display:flex;align-items:center;justify-content:center;min-height:100vh"><div style="text-align:center"><h1>Offline</h1><p>Your changes are saved locally.</p><button onclick="location.reload()">Retry</button></div></body></html>',
        { headers: { 'Content-Type': 'text/html' } })
    )
  );
});

// Background Sync
self.addEventListener('sync', (event) => {
  if (event.tag.startsWith('wl-sync:')) {
    event.waitUntil(replayMutations(event.tag));
  }
});

// Push Notifications
self.addEventListener('push', (event) => {
  const data = event.data?.json() || { title: 'Notification', body: '' };
  event.waitUntil(self.registration.showNotification(data.title, {
    body: data.body, icon: data.icon, data: data.payload || {},
  }));
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(self.clients.openWindow(event.notification.data?.url || '/'));
});

self.addEventListener('message', (event) => {
  if (event.data?.type === 'relay') {
    self.clients.matchAll({ type: 'window' }).then(clients => {
      for (const c of clients) if (c.id !== event.source?.id) c.postMessage(event.data.payload);
    });
  }
});

async function replayMutations(tag) {
  const db = await new Promise((res, rej) => {
    const req = indexedDB.open('wl-sync-queue', 1);
    req.onupgradeneeded = () => {
      const db = req.result;
      if (!db.objectStoreNames.contains('mutations')) {
        const s = db.createObjectStore('mutations', { keyPath: 'id', autoIncrement: true });
        s.createIndex('tag', 'tag'); s.createIndex('timestamp', 'timestamp');
      }
    };
    req.onsuccess = () => res(req.result);
    req.onerror = () => rej(req.error);
  });
  const tx = db.transaction('mutations', 'readonly');
  const items = await new Promise((res, rej) => {
    const r = tx.objectStore('mutations').index('tag').getAll(tag);
    r.onsuccess = () => res(r.result); r.onerror = () => rej(r.error);
  });
  for (const item of items) {
    try {
      const resp = await fetch(item.url, {
        method: item.method || 'POST',
        headers: item.headers || { 'Content-Type': 'application/json' },
        body: item.body ? JSON.stringify(item.body) : undefined,
      });
      if (resp.ok || resp.status < 500) {
        const del = db.transaction('mutations', 'readwrite');
        del.objectStore('mutations').delete(item.id);
        await new Promise(r => { del.oncomplete = r; });
      } else break;
    } catch { break; }
  }
}
