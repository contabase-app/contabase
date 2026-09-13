(function() {
  function initNotificationSwipe() {
    var motion = window.cbRuntimeMotion;
    if (!motion || !motion.supported) return;
    document.querySelectorAll('[data-swipe-notification]').forEach(function(card) {
      if (card.dataset.notificationSwipeInit === 'true') return;
      var delBtn = card.querySelector('[data-swipe-delete]');
      if (!delBtn) return;
      card.dataset.notificationSwipeInit = 'true';

      var startX = 0, currentX = 0, swiping = false;

      card.addEventListener('touchstart', function(e) {
        startX = e.touches[0].clientX;
        currentX = 0;
        swiping = true;
        motion.cancel(card);
      }, { passive: true });

      card.addEventListener('touchmove', function(e) {
        if (!swiping) return;
        var diff = e.touches[0].clientX - startX;
        if (diff > 0) diff = 0;
        currentX = diff;
        motion.hold(card, { transform: 'translateX(' + currentX + 'px)', opacity: '1' });
      }, { passive: true });

      card.addEventListener('touchend', function() {
        swiping = false;
        if (currentX < -100) {
          motion.exit(card, { transform: 'translateX(-100%)', opacity: '0' }, {
            duration: 300,
            easing: 'ease',
            complete: function() { delBtn.click(); }
          });
        } else {
          motion.toBase(card, { transform: 'translateX(0px)', opacity: '1' }, {
            duration: 300,
            easing: 'ease'
          });
        }
      });

      card.addEventListener('touchcancel', function() {
        swiping = false;
        motion.toBase(card, { transform: 'translateX(0px)', opacity: '1' }, {
          duration: 300,
          easing: 'ease'
        });
      });
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initNotificationSwipe);
  } else {
    initNotificationSwipe();
  }

  document.body.addEventListener('htmx:afterSettle', function() {
    initNotificationSwipe();
  });
})();
