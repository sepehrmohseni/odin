// Shared behaviour for the Odin admin: navigation state, theme, copy buttons, optional links,
// and small helpers the pages use to render live metrics.
(function () {
  'use strict';

  // Highlight the navigation entry whose path (or data-nav prefixes) best matches this page.
  function markActiveNav() {
    var path = window.location.pathname;
    var best = null;
    var bestLen = -1;
    document.querySelectorAll('.odin-nav a').forEach(function (a) {
      var prefixes = (a.getAttribute('data-nav') || a.getAttribute('href')).split(' ');
      prefixes.forEach(function (p) {
        if ((path === p || path.indexOf(p + '/') === 0) && p.length > bestLen) {
          best = a;
          bestLen = p.length;
        }
      });
    });
    if (best) best.classList.add('active');
  }

  function themeLabel() {
    var dark = document.documentElement.getAttribute('data-bs-theme') === 'dark';
    document.querySelectorAll('[data-theme-toggle] span').forEach(function (s) {
      s.textContent = dark ? 'Light theme' : 'Dark theme';
    });
  }

  function setupTheme() {
    themeLabel();
    document.querySelectorAll('[data-theme-toggle]').forEach(function (b) {
      b.addEventListener('click', function () {
        var next = document.documentElement.getAttribute('data-bs-theme') === 'dark' ? 'light' : 'dark';
        document.documentElement.setAttribute('data-bs-theme', next);
        localStorage.setItem('odin-theme', next);
        themeLabel();
        document.dispatchEvent(new CustomEvent('odin:theme'));
      });
    });
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text);
    }
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand('copy');
    } finally {
      document.body.removeChild(ta);
    }
    return Promise.resolve();
  }

  // <button data-copy="text">: copies the text and confirms with a check mark.
  document.addEventListener('click', function (e) {
    var btn = e.target.closest('[data-copy]');
    if (!btn) return;
    copyText(btn.getAttribute('data-copy')).then(function () {
      var icon = btn.querySelector('.bi');
      if (!icon) return;
      var old = icon.className;
      icon.className = 'bi bi-check2';
      btn.classList.add('odin-copied');
      setTimeout(function () {
        icon.className = old;
        btn.classList.remove('odin-copied');
      }, 1200);
    });
  });

  // A request that finds the session expired goes back to the login page.
  document.addEventListener('htmx:responseError', function (e) {
    var s = e.detail.xhr.status;
    if (s === 401) window.location.href = '/admin/login';
  });

  // ---------- helpers for the pages ----------

  function escapeHTML(s) {
    return String(s == null ? '' : s).replace(/[&<>"']/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c];
    });
  }

  function fmtNumber(n) {
    n = Number(n) || 0;
    if (n >= 1e6) return (n / 1e6).toFixed(n >= 1e7 ? 0 : 1) + 'M';
    if (n >= 1e4) return (n / 1e3).toFixed(n >= 1e5 ? 0 : 1) + 'k';
    return n.toLocaleString();
  }

  function fmtMs(ms) {
    ms = Number(ms) || 0;
    if (ms >= 1000) return (ms / 1000).toFixed(2) + ' s';
    if (ms >= 100) return ms.toFixed(0) + ' ms';
    if (ms >= 10) return ms.toFixed(1) + ' ms';
    return ms.toFixed(2) + ' ms';
  }

  function relTime(t) {
    var d = (Date.now() - new Date(t).getTime()) / 1000;
    if (!isFinite(d) || d < 0) return '';
    if (d < 5) return 'just now';
    if (d < 60) return Math.floor(d) + ' s ago';
    if (d < 3600) return Math.floor(d / 60) + ' min ago';
    if (d < 86400) return Math.floor(d / 3600) + ' h ago';
    return Math.floor(d / 86400) + ' d ago';
  }

  function since(t) {
    var d = Math.max(0, (Date.now() - new Date(t).getTime()) / 1000);
    if (d < 60) return Math.floor(d) + ' s';
    if (d < 3600) return Math.floor(d / 60) + ' min';
    if (d < 86400) return Math.floor(d / 3600) + ' h ' + Math.floor((d % 3600) / 60) + ' min';
    return Math.floor(d / 86400) + ' d ' + Math.floor((d % 86400) / 3600) + ' h';
  }

  function statusClass(code) {
    return 's' + String(code).charAt(0);
  }

  // Draws values as a filled line into an <svg>, scaled to its width.
  function sparkline(svg, values, opts) {
    if (!svg) return;
    opts = opts || {};
    var w = svg.clientWidth || 200;
    var h = svg.clientHeight || 28;
    var max = Math.max.apply(null, values.concat([opts.min || 1]));
    var step = values.length > 1 ? w / (values.length - 1) : w;
    var pts = values.map(function (v, i) {
      return [i * step, h - 2 - (v / max) * (h - 4)];
    });
    var line = pts.map(function (p, i) { return (i ? 'L' : 'M') + p[0].toFixed(1) + ' ' + p[1].toFixed(1); }).join(' ');
    var area = line + ' L' + w + ' ' + h + ' L0 ' + h + ' Z';
    svg.setAttribute('viewBox', '0 0 ' + w + ' ' + h);
    svg.innerHTML =
      '<path d="' + area + '" fill="var(--odin-accent-soft)" />' +
      '<path d="' + line + '" fill="none" stroke="var(--odin-accent)" stroke-width="1.5" stroke-linejoin="round" />';
  }

  // Calls fn with /admin/api/monitoring/metrics now and every intervalMs while the tab is visible.
  function pollMetrics(fn, intervalMs) {
    var timer = null;
    function tick() {
      fetch('/admin/api/monitoring/metrics', { cache: 'no-store', credentials: 'same-origin' })
        .then(function (r) {
          if (r.status === 401) {
            window.location.href = '/admin/login';
            return null;
          }
          return r.ok ? r.json() : null;
        })
        .then(function (m) {
          if (m) fn(m);
        })
        .catch(function () {});
    }
    function start() {
      if (timer) return;
      tick();
      timer = setInterval(tick, intervalMs || 5000);
    }
    function stop() {
      clearInterval(timer);
      timer = null;
    }
    document.addEventListener('visibilitychange', function () {
      if (document.hidden) stop();
      else start();
    });
    start();
  }

  window.Odin = {
    escapeHTML: escapeHTML,
    fmtNumber: fmtNumber,
    fmtMs: fmtMs,
    relTime: relTime,
    since: since,
    statusClass: statusClass,
    sparkline: sparkline,
    pollMetrics: pollMetrics,
    copyText: copyText,
  };

  document.addEventListener('DOMContentLoaded', function () {
    markActiveNav();
    setupTheme();
  });
})();
