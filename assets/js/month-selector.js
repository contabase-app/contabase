// Shared month selector behavior.
// Keeps the selected period visible after direct loads and HTMX/OOB updates.
(function () {
    'use strict';

    function prefersReducedMotion() {
        return window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    }

    function revealActiveMonth(selector) {
        var viewport = selector.querySelector('[data-month-rail-viewport]');
        var active = selector.querySelector('[data-month-active="true"]');
        if (!viewport || !active) return;

        window.requestAnimationFrame(function () {
            var target = Math.max(0, active.offsetLeft - ((viewport.clientWidth - active.offsetWidth) / 2));
            viewport.scrollTo({
                left: target,
                behavior: prefersReducedMotion() ? 'auto' : 'smooth'
            });
        });
    }

    function initialize(root) {
        (root || document).querySelectorAll('[data-month-selector]').forEach(revealActiveMonth);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function () { initialize(document); }, { once: true });
    } else {
        initialize(document);
    }

    document.body.addEventListener('htmx:afterSwap', function () {
        initialize(document);
    });
}());
