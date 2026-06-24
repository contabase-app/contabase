(function() {
  function initNotificationSwipe() {
    document.querySelectorAll('[data-swipe-notification]').forEach(function(card) {
      var delBtn = card.querySelector('[data-swipe-delete]');
      if (!delBtn) return;

      var startX = 0, currentX = 0, swiping = false;

      card.addEventListener('touchstart', function(e) {
        startX = e.touches[0].clientX;
        swiping = true;
        card.style.transition = 'none';
      }, { passive: true });

      card.addEventListener('touchmove', function(e) {
        if (!swiping) return;
        var diff = e.touches[0].clientX - startX;
        if (diff > 0) diff = 0;
        currentX = diff;
        card.style.transform = 'translateX(' + currentX + 'px)';
      }, { passive: true });

      card.addEventListener('touchend', function() {
        swiping = false;
        if (currentX < -100) {
          card.style.transition = 'all 0.3s ease';
          card.style.transform = 'translateX(-100%)';
          card.style.opacity = '0';
          setTimeout(function() {
            delBtn.click();
          }, 300);
        } else {
          card.style.transition = 'all 0.3s ease';
          card.style.transform = 'translateX(0)';
        }
      });

      card.addEventListener('touchcancel', function() {
        swiping = false;
        card.style.transition = 'all 0.3s ease';
        card.style.transform = 'translateX(0)';
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
