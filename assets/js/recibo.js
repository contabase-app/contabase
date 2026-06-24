// Recibo page — standalone, does not depend on layout.html scripts
(function() {
    // Image avatar fallback
    document.addEventListener('error', function(e) {
        var img = e.target;
        if (!img || img.tagName !== 'IMG') return;
        if (!img.hasAttribute('data-avatar-fallback')) return;
        img.style.display = 'none';
        var fallback = img.nextElementSibling;
        if (fallback && fallback.hasAttribute('data-avatar-text')) {
            fallback.style.display = '';
        }
    }, true);

    // data-onclick delegation (replaces inline onclick)
    document.addEventListener('click', function(e) {
        var el = e.target.closest('[data-onclick]');
        if (!el) return;
        var name = el.getAttribute('data-onclick');
        var fn = window[name];
        if (typeof fn === 'function') fn();
    });
})();
