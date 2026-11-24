(() => {
  "use strict";
  const originalFetch = window.fetch.bind(window);
  let capturedAt = 0;
  function capture() {
    if (Date.now() - capturedAt < 30000) return;
    capturedAt = Date.now();
    let timezone = "";
    try { timezone = Intl.DateTimeFormat().resolvedOptions().timeZone || ""; } catch (_) {}
    const device = {
      browser: navigator.userAgent.slice(0, 256),
      platform: (navigator.userAgentData?.platform || navigator.platform || "").slice(0, 256),
      language: (navigator.language || "").slice(0, 256),
      timezone,
      screen: `${screen.width}x${screen.height}`,
      color_depth: String(screen.colorDepth),
      touch: String(navigator.maxTouchPoints || 0)
    };
    originalFetch("/_siteadmin_probe/device", {
      method: "POST", credentials: "same-origin", keepalive: true,
      headers: { "Content-Type": "application/json" }, body: JSON.stringify(device)
    }).catch(() => {});
  }
  function authPage() {
    if (/^\/(login|register)(\/|$)/.test(location.pathname)) capture();
  }
  window.fetch = function(input, init) {
    try {
      const target = new URL(typeof input === "string" || input instanceof URL ? input : input.url, location.href);
      if (target.origin === location.origin &&
          /^\/api\/user\/(login(?:\/(?:2fa|verify|passkey\/finish))?|register|passkey\/login\/finish)$/.test(target.pathname)) {
        capture();
      }
    } catch (_) {}
    return originalFetch(input, init);
  };
  document.addEventListener("submit", authPage, true);
  window.addEventListener("popstate", authPage);
  for (const method of ["pushState", "replaceState"]) {
    const original = history[method];
    history[method] = function(...args) {
      const result = original.apply(this, args);
      authPage();
      return result;
    };
  }
  authPage();
})();
