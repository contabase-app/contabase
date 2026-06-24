(function() {
  function animateBalance(el) {
    var rawStr = el.getAttribute('data-balance-raw') || '';
    var isNegative = rawStr.startsWith('-');
    var cleanStr = rawStr.replace(/[^0-9]/g, '');
    var target = parseInt(cleanStr) || 0;
    var current = 0;
    var duration = 850;
    var startTime = null;
    function step(timestamp) {
      if (!startTime) startTime = timestamp;
      var progress = Math.min((timestamp - startTime) / duration, 1);
      current = Math.floor(progress * target);
      var formatted = current.toLocaleString('pt-BR');
      el.textContent = (isNegative ? '-' : '') + formatted;
      if (progress < 1) requestAnimationFrame(step);
    }
    requestAnimationFrame(step);
  }

  function initBalances() {
    document.querySelectorAll('[data-balance-counter]').forEach(function(el) {
      animateBalance(el);
    });
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initBalances);
  } else {
    initBalances();
  }

  document.body.addEventListener('htmx:afterSettle', function() {
    initBalances();
  });
})();
