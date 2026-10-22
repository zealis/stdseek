(function () {
  'use strict';
  var html = document.documentElement;
  var toggle = document.querySelector('[data-dom-id="theme-toggle"]');
  if (!toggle) return;

  function getPreferredTheme() {
    var stored = localStorage.getItem('owl-theme');
    if (stored === 'light' || stored === 'dark') return stored;
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  }

  function applyTheme(theme) {
    html.classList.toggle('dark', theme === 'dark');
    html.classList.toggle('light', theme === 'light');
    toggle.setAttribute('aria-checked', theme === 'light' ? 'true' : 'false');
    toggle.setAttribute('aria-label', theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode');
    localStorage.setItem('owl-theme', theme);
  }

  applyTheme(getPreferredTheme());

  toggle.addEventListener('click', function () {
    var isDark = html.classList.contains('dark');
    applyTheme(isDark ? 'light' : 'dark');
  });

  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', function (e) {
    if (!localStorage.getItem('owl-theme')) {
      applyTheme(e.matches ? 'dark' : 'light');
    }
  });
})();
