// Shared credit card scheme.
// Institutions with a luminous color receive dark ink (data-card-scheme="light")
// so text keeps AA contrast on the tonal face. One implementation for every
// consumer: Dashboard (.wallet-card) and Faturas hub (.cb-v2-credit-card).
(function () {
    'use strict';

    function channelToLinear(channel) {
        return channel <= 0.04045 ? channel / 12.92 : Math.pow((channel + 0.055) / 1.055, 2.4);
    }

    function schemeForColor(raw) {
        var match = /^#([0-9a-f]{6})$/i.exec(String(raw || '').trim());
        if (!match) return 'dark';
        var value = parseInt(match[1], 16);
        var luminance = 0.2126 * channelToLinear(((value >> 16) & 255) / 255) +
            0.7152 * channelToLinear(((value >> 8) & 255) / 255) +
            0.0722 * channelToLinear((value & 255) / 255);
        return luminance >= 0.45 ? 'light' : 'dark';
    }

    function applyCardSchemes(root) {
        (root || document).querySelectorAll('[data-cb-color]').forEach(function (card) {
            if (!card.classList || !card.classList.contains('cb-card-face')) return;
            card.setAttribute('data-card-scheme', schemeForColor(card.getAttribute('data-cb-color')));
        });
    }

    window.applyCardSchemes = applyCardSchemes;
    window.creditCardSchemeForColor = schemeForColor;

    function initialize() {
        applyCardSchemes(document);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize);
    } else {
        initialize();
    }
    document.body.addEventListener('htmx:afterSwap', initialize);
    document.body.addEventListener('htmx:oobAfterSwap', initialize);
    document.body.addEventListener('workspaceColorsChanged', initialize);
}());
