(function() {
  function initNotificationSwipe() {
    var motion = window.cbRuntimeMotion;
    if (!motion || !motion.supported) return;
    document.querySelectorAll('[data-swipe-notification]').forEach(function(card) {
      if (card.dataset.notificationSwipeInit === 'true') return;
      var delBtn = card.querySelector('[data-swipe-delete]');
      if (!delBtn) return;
      card.dataset.notificationSwipeInit = 'true';

      var startX = 0, currentX = 0, swiping = false, moved = false, suppressClick = false;

      card.addEventListener('touchstart', function(e) {
        startX = e.touches[0].clientX;
        currentX = 0;
        swiping = true;
        moved = false;
        suppressClick = false;
        motion.cancel(card);
      }, { passive: true });

      card.addEventListener('touchmove', function(e) {
        if (!swiping) return;
        var diff = e.touches[0].clientX - startX;
        if (Math.abs(diff) > 10) moved = true;
        if (diff > 0) diff = 0;
        currentX = diff;
        motion.hold(card, { transform: 'translateX(' + currentX + 'px)', opacity: '1' });
      }, { passive: true });

      card.addEventListener('touchend', function() {
        swiping = false;
        if (moved) suppressClick = true;
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

      // A swipe must never navigate through the card's stretched link. Other
      // controls (delete/payment) keep their own handlers and HTMX contract.
      card.addEventListener('click', function(event) {
        if (!suppressClick) return;
        suppressClick = false;
        var link = event.target && event.target.closest
          ? event.target.closest('[data-notification-open]')
          : null;
        if (!link) return;
        event.preventDefault();
        event.stopPropagation();
      }, true);

      card.addEventListener('touchcancel', function() {
        swiping = false;
        moved = false;
        suppressClick = false;
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
