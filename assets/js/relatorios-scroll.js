// Relatórios — horizontal scroll affordance
// Mostra a dica "Deslize para ver mais colunas" apenas quando a região realmente
// transborda e ainda não chegou ao fim. Reaproveita os containers já existentes
// (.reports-table-scroll/.reports-dre-scroll) e não altera a tabela semântica.
(function () {
    'use strict';

    var SCROLL_SELECTOR = '.reports-table-scroll, .reports-dre-scroll';

    function hintFor(region) {
        var hint = region.previousElementSibling;
        return hint && hint.hasAttribute('data-scroll-hint') ? hint : null;
    }

    function update(region) {
        var hint = hintFor(region);
        if (!hint) return;
        var scrollable = region.scrollWidth > region.clientWidth + 1;
        var atEnd = region.scrollLeft + region.clientWidth >= region.scrollWidth - 2;
        hint.hidden = !scrollable || atEnd;
    }

    function init(root) {
        var scope = root && typeof root.querySelectorAll === 'function' ? root : document;
        scope.querySelectorAll(SCROLL_SELECTOR).forEach(function (region) {
            if (region.dataset.scrollHintReady === 'true') return;
            region.dataset.scrollHintReady = 'true';
            region.addEventListener('scroll', function () { update(region); }, { passive: true });
            update(region);
        });
    }

    function updateAll() {
        document.querySelectorAll(SCROLL_SELECTOR).forEach(update);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', function () { init(document); });
    } else {
        init(document);
    }

    document.body.addEventListener('htmx:afterSwap', function () {
        init(document);
        window.setTimeout(updateAll, 60);
    });

    window.addEventListener('resize', updateAll);
    window.addEventListener('load', updateAll);
})();
