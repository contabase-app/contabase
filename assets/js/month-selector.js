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
            // offsetLeft is relative to the offsetParent (usually BODY), not to
            // the scroll viewport. Measure both rects so the active month is
            // centered on every page that shares this rail.
            var viewportRect = viewport.getBoundingClientRect();
            var activeRect = active.getBoundingClientRect();
            var target = Math.max(0, viewport.scrollLeft + (activeRect.left - viewportRect.left) - ((viewport.clientWidth - activeRect.width) / 2));
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
