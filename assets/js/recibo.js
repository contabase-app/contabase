// Recibo page — standalone, does not depend on authenticated-shell scripts.
(function() {
    function showFallback(img) {
        if (!img || !img.parentElement) return;
        img.classList.add('receipt-hidden');
        var fallback = img.nextElementSibling;
        if (fallback && fallback.hasAttribute('data-avatar-text')) {
            fallback.classList.remove('receipt-hidden');
        }
    }

    // Image avatar fallback. The listener covers late failures; the DOM-ready
    // sweep covers an image that had already failed before this script ran,
    // which is the common case for cached 404/expired logo URLs.
    document.addEventListener('error', function(e) {
        var img = e.target;
        if (!img || img.tagName !== 'IMG') return;
        if (!img.hasAttribute('data-avatar-fallback')) return;
        showFallback(img);
    }, true);

    function settleReceiptLogos() {
        document.querySelectorAll('img[data-receipt-logo]').forEach(function(img) {
            if (img.complete && img.naturalWidth === 0) showFallback(img);
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', settleReceiptLogos, { once: true });
    } else {
        settleReceiptLogos();
    }

    // data-onclick delegation (replaces inline onclick)
    document.addEventListener('click', function(e) {
        var el = e.target.closest('[data-onclick]');
        if (!el) return;
        var name = el.getAttribute('data-onclick');
        var fn = window[name];
        if (typeof fn === 'function') fn();
    });
})();
