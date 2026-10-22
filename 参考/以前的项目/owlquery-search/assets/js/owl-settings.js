/* OwlQuery Settings Page Scripts */

(function () {
  'use strict';
  var toggles = document.querySelectorAll('.owl-toggle');
  toggles.forEach(function (toggle) {
    var input = toggle.querySelector('input[type="checkbox"]');
    var track = toggle.querySelector('.owl-toggle__track');
    var thumb = toggle.querySelector('.owl-toggle__thumb');

    // Restore from localStorage on page load
    if (input.id === 'toggle-show-stats') {
      var saved = localStorage.getItem('owl-show-stats');
      if (saved !== null) {
        input.checked = saved === '1';
      }
    }

    function updateVisual() {
      if (input.checked) {
        track.style.background = '#86d67a';
        track.style.boxShadow = 'inset 0 2px 4px rgba(90, 158, 30, 0.20)';
        track.style.borderColor = '#6fba2c';
        thumb.style.left = 'calc(100% - 24px)';
        thumb.style.borderColor = '#6fba2c';
        thumb.style.background = 'rgb(247,243,223)';
      } else {
        track.style.background = '#d4c9b4';
        track.style.boxShadow = 'inset 0 2px 4px rgba(114, 93, 66, 0.15)';
        track.style.borderColor = '#c4b89e';
        thumb.style.left = '2px';
        thumb.style.borderColor = '#c4b89e';
        thumb.style.background = 'rgb(247,243,223)';
      }
    }

    updateVisual();

    input.addEventListener('change', function () {
      updateVisual();
      if (input.id === 'toggle-show-stats') {
        localStorage.setItem('owl-show-stats', input.checked ? '1' : '0');
      }
    });
  });
})();
