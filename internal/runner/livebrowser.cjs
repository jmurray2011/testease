'use strict';
// testease live browser hook.
//
// testease loads this with NODE_OPTIONS=--require into every node process of a
// drive step that was started with --live-browser. It saves what the most
// recently active page shows, about once a second, as a JPEG in
// TESTEASE_LIVE_DIR, so the desktop app can show the browser while the step
// runs. The frames live in the step's workspace and go with it: they never
// enter the record.
//
// It must never change what the suite does. Every hook is wrapped so that a
// failure here leaves Playwright exactly as it was, and the timer is unref'd
// so it never keeps a process alive.
(function () {
  const dir = process.env.TESTEASE_LIVE_DIR;
  if (!dir) return;

  const fs = require('fs');
  const path = require('path');
  const Module = require('module');

  const every = Number(process.env.TESTEASE_LIVE_EVERY_MS) || 1000;
  const frame = path.join(dir, `frame-${process.pid}.jpg`);
  const about = path.join(dir, `frame-${process.pid}.json`);
  const latest = path.join(dir, 'latest.jpg');
  const latestAbout = path.join(dir, 'latest.json');
  // Each process stages under its own name, so two never write one file.
  const mine = `.${process.pid}.tmp`;
  const contexts = new Set();
  let current = null;
  let busy = false;

  function watchPage(page) {
    try {
      current = page;
      page.on('framenavigated', (f) => { if (f === page.mainFrame()) current = page; });
      page.on('close', () => { if (current === page) current = null; });
    } catch (_) { /* not a page we understand */ }
  }

  function watchContext(context) {
    try {
      if (!context || contexts.has(context)) return;
      contexts.add(context);
      context.pages().forEach(watchPage);
      context.on('page', watchPage);
      context.on('close', () => contexts.delete(context));
    } catch (_) { /* not a context we understand */ }
  }

  function watchBrowser(browser) {
    try {
      browser.contexts().forEach(watchContext);
      for (const name of ['newContext', 'newPage']) {
        const original = browser[name];
        if (typeof original !== 'function') continue;
        browser[name] = async function (...args) {
          const made = await original.apply(this, args);
          try { watchContext(name === 'newPage' ? made.context() : made); } catch (_) { /* keep going */ }
          return made;
        };
      }
    } catch (_) { /* not a browser we understand */ }
  }

  function page() {
    if (current && !current.isClosed()) return current;
    for (const context of contexts) {
      const open = context.pages().filter((p) => !p.isClosed());
      if (open.length) return open[open.length - 1];
    }
    return null;
  }

  async function capture() {
    if (busy) return;
    const p = page();
    if (!p) return;
    busy = true;
    try {
      const shoot = () => p.screenshot({ type: 'jpeg', quality: 60, timeout: every * 2, animations: 'allow', caret: 'initial' });
      // Marked internal, the way Playwright marks its own housekeeping, so
      // the frames never show as steps in the suite's report or trace. Older
      // releases take a boolean there; an object is truthy to them too.
      const body = typeof p._wrapApiCall === 'function'
        ? await p._wrapApiCall(shoot, { internal: true })
        : await shoot();
      fs.writeFileSync(frame + '.tmp', body);
      fs.renameSync(frame + '.tmp', frame);
      const said = JSON.stringify({ url: p.url(), at: new Date().toISOString() });
      fs.writeFileSync(about + '.tmp', said);
      fs.renameSync(about + '.tmp', about);
      // The same frame under one fixed name, for a runner that has to fetch
      // it from another machine by name rather than list the directory.
      fs.writeFileSync(latest + mine, body);
      fs.renameSync(latest + mine, latest);
      fs.writeFileSync(latestAbout + mine, said);
      fs.renameSync(latestAbout + mine, latestAbout);
    } catch (_) {
      // A page mid-navigation, closing, or busy: the next tick tries again.
    } finally {
      busy = false;
    }
  }

  function patchBrowserType(type) {
    try {
      const proto = Object.getPrototypeOf(type);
      if (!proto || proto.__testeaseLive) return;
      Object.defineProperty(proto, '__testeaseLive', { value: true });
      for (const name of ['launch', 'connect', 'connectOverCDP']) {
        const original = proto[name];
        if (typeof original !== 'function') continue;
        proto[name] = async function (...args) {
          const browser = await original.apply(this, args);
          watchBrowser(browser);
          return browser;
        };
      }
      const persistent = proto.launchPersistentContext;
      if (typeof persistent === 'function') {
        proto.launchPersistentContext = async function (...args) {
          const context = await persistent.apply(this, args);
          watchContext(context);
          return context;
        };
      }
    } catch (_) { /* leave it as it was */ }
  }

  function patchExports(exported) {
    for (const candidate of [exported, exported && exported.default]) {
      if (!candidate || (typeof candidate !== 'object' && typeof candidate !== 'function')) continue;
      for (const name of ['chromium', 'firefox', 'webkit']) {
        let type;
        try { type = candidate[name]; } catch (_) { continue; }
        if (type && typeof type.launch === 'function') patchBrowserType(type);
      }
    }
  }

  const load = Module._load;
  Module._load = function (request, ...rest) {
    const exported = load.call(this, request, ...rest);
    if (typeof request === 'string' && request.includes('playwright')) patchExports(exported);
    return exported;
  };

  try { fs.mkdirSync(dir, { recursive: true }); } catch (_) { /* the drive made it */ }
  setInterval(capture, every).unref();
})();
