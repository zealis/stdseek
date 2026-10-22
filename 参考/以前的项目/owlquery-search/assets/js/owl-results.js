/* OwlQuery Results Page Scripts */

/* === Filter Tab Activation === */
function activateTab(btn) {
  var group = btn.parentElement;
  var siblings = group.querySelectorAll('.filter-tab');
  for (var i = 0; i < siblings.length; i++) {
    siblings[i].style.background = 'var(--owl-card)';
    siblings[i].style.color = 'var(--owl-foreground)';
    siblings[i].style.borderColor = 'var(--owl-glass-border)';
    siblings[i].classList.remove('active');
  }
  btn.style.background = '#0CC0B5';
  btn.style.color = '#FFF9E3';
  btn.style.borderColor = '#0CC0B5';
  btn.classList.add('active');
}

/* === Pagination Activation === */
function activatePage(btn) {
  var group = btn.parentElement;
  var siblings = group.querySelectorAll('.page-btn');
  for (var i = 0; i < siblings.length; i++) {
    siblings[i].style.background = 'var(--owl-card)';
    siblings[i].style.color = 'var(--owl-foreground)';
    siblings[i].style.borderColor = 'var(--owl-glass-border)';
    siblings[i].classList.remove('active');
  }
  btn.style.background = '#0CC0B5';
  btn.style.color = '#FFF9E3';
  btn.style.borderColor = '#0CC0B5';
  btn.classList.add('active');
}

/* === Feedback Button Injection (XSS-safe) === */
(function () {
  document.querySelectorAll('.owl-result-card').forEach(function (card) {
    var btn = document.createElement('button');
    btn.className = 'owl-feedback-btn';
    btn.setAttribute('aria-label', '\u53cd\u9988');
    btn.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="3" r="1.5"/><circle cx="8" cy="8" r="1.5"/><circle cx="8" cy="13" r="1.5"/></svg>';
    card.appendChild(btn);

    btn.addEventListener('click', function (e) {
      e.stopPropagation();
      // Close any existing dropdown
      document.querySelectorAll('.owl-feedback-dropdown').forEach(function (d) { d.remove(); });

      var rect = btn.getBoundingClientRect();
      var dropdown = document.createElement('div');
      dropdown.className = 'owl-feedback-dropdown';
      dropdown.style.top = (rect.bottom + 4) + 'px';
      dropdown.style.left = (rect.right - 140) + 'px';
      if (rect.right - 140 < 4) dropdown.style.left = '4px';
      var options = ['\u76f8\u5173\u63a8\u8350', '\u4e0d\u611f\u5174\u8da3', '\u53cd\u9988\u610f\u89c1'];
      options.forEach(function (label) {
        var opt = document.createElement('div');
        opt.className = 'owl-feedback-option';
        // Use textContent for safety (no innerHTML with user data)
        opt.textContent = label;
        opt.addEventListener('click', function (ev) {
          ev.stopPropagation();
          dropdown.remove();
          // Show checkmark (hardcoded SVG, safe)
          btn.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="var(--owl-primary)"><path d="M3 8l3.5 3.5L13 5" stroke="var(--owl-primary)" stroke-width="2" fill="none" stroke-linecap="round" stroke-linejoin="round"/></svg>';
          setTimeout(function () {
            btn.innerHTML = '<svg width="16" height="16" viewBox="0 0 16 16" fill="currentColor"><circle cx="8" cy="3" r="1.5"/><circle cx="8" cy="8" r="1.5"/><circle cx="8" cy="13" r="1.5"/></svg>';
          }, 1500);
        });
        dropdown.appendChild(opt);
      });
      document.body.appendChild(dropdown);
    });
  });

  /* === Close dropdowns on click or scroll (rAF-throttled) === */
  function closeDropdowns() {
    document.querySelectorAll('.owl-feedback-dropdown').forEach(function (d) { d.remove(); });
  }
  document.addEventListener('click', closeDropdowns);

  var scrollTick = false;
  document.addEventListener('scroll', function () {
    if (!scrollTick) {
      scrollTick = true;
      requestAnimationFrame(function () {
        closeDropdowns();
        scrollTick = false;
      });
    }
  }, true);

  /* === Search Stats localStorage reader === */
  (function () {
    function applyStats(show) {
      var statsEl = document.getElementById('owl-search-stats');
      if (statsEl) statsEl.style.display = show ? '' : 'none';
    }
    var showStats = localStorage.getItem('owl-show-stats') !== '0';
    applyStats(showStats);
    // Listen for changes from settings page (same tab via storage event)
    window.addEventListener('storage', function (e) {
      if (e.key === 'owl-show-stats') {
        applyStats(e.newValue !== '0');
      }
    });
  })();
})();
