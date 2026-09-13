(function () {
    'use strict';

    var colorPattern = /^#[0-9a-f]{6}$/i;

    function normalizeColor(raw, fallback) {
        var color = String(raw || '').trim();
        if (!colorPattern.test(color)) color = fallback || '#6B7280';
        if (!colorPattern.test(color)) color = '#6B7280';
        return color.toUpperCase();
    }

    window.applyRuntimeColor = function (element, color, fallback) {
        if (!element) return;
        element.dataset.cbColor = normalizeColor(color, fallback);
    };

    window.clearRuntimeColor = function (element) {
        if (!element) return;
        delete element.dataset.cbColor;
    };

    document.body.addEventListener('workspaceColorsChanged', function () {
        var link = document.querySelector('link[data-workspace-colors]');
        if (!link) return;
        var url = new URL('/workspace/colors.css', window.location.origin);
        url.searchParams.set('v', String(Date.now()));
        link.href = url.pathname + url.search;
    });
})();
