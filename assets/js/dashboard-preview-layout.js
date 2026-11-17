/*
 * Dashboard Preview — ordem responsiva acessível (R1.3-B10.1).
 *
 * O SSR entrega os cards na ordem canônica mobile. Este script monta as duas
 * regiões verticais do desktop usando os MESMOS nós (sem duplicar cards) e
 * devolve os cards à ordem canônica ao voltar para <=1199px. A ordem do DOM
 * acompanha a ordem visual em cada breakpoint; não há tabindex positivo nem
 * reordenação apenas por CSS.
 *
 * Carregado de forma síncrona logo após #dashboard-content para montar o
 * desktop antes do primeiro paint. Se o script falhar, o CSS revela o grid em
 * coluna única após 1,5s e nada fica oculto.
 */
(function () {
    'use strict';

    var DESKTOP_QUERY = '(min-width: 1200px)';
    var ORDER = ['balance', 'finance', 'cashflow', 'accounts', 'commitments', 'cards', 'categories', 'goals'];
    var LEFT = ['balance', 'accounts', 'cards', 'commitments'];
    var RIGHT = ['finance', 'cashflow'];
    var PAIR = ['categories', 'goals'];

    var media = window.matchMedia(DESKTOP_QUERY);
    var bound = null;
    var lastFocused = null;

    function teardown() {
        if (!bound) {
            return;
        }
        if (media.removeEventListener) {
            media.removeEventListener('change', bound.onChange);
        } else if (media.removeListener) {
            media.removeListener(bound.onChange);
        }
        if (bound.observer && typeof bound.observer.disconnect === 'function') {
            bound.observer.disconnect();
        }
        if (bound.onFocusIn && bound.grid.removeEventListener) {
            bound.grid.removeEventListener('focusin', bound.onFocusIn);
        }
        if (bound.onPointerDown) {
            document.removeEventListener('pointerdown', bound.onPointerDown);
        }
        if (bound.onResize) {
            window.removeEventListener('resize', bound.onResize);
        }
        bound = null;
    }

    function init() {
        var grid = document.getElementById('dashboard-content');
        if (!grid || !grid.classList.contains('cbp-grid--contract')) {
            return;
        }
        // O settle do HTMX pode sobrescrever a class list do grid novo. Se o
        // mesmo grid já está pronto mas perdeu a composição de regiões, ele é
        // reconstruído; sem isso, um refresh deixaria os cards empilhados.
        if (grid.getAttribute('data-cbp-layout') === 'ready') {
            var composed = grid.classList.contains('is-regions');
            if (composed === media.matches) {
                grid.classList.remove('is-layout-pending');
                return;
            }
            grid.removeAttribute('data-cbp-layout');
        }
        teardown();

        var left = grid.querySelector('[data-cbp-region="left"]');
        var right = grid.querySelector('[data-cbp-region="right"]');
        var pair = grid.querySelector('[data-cbp-region-pair]');
        if (!left || !right || !pair) {
            grid.classList.remove('is-layout-pending');
            return;
        }

        var cards = {};
        ORDER.forEach(function (key) {
            var el = grid.querySelector('[data-cbp-section="' + key + '"]');
            if (el) {
                cards[key] = el;
            }
        });

        if (Object.keys(cards).length !== ORDER.length) {
            grid.classList.remove('is-layout-pending');
            return;
        }

        function move(parent, key) {
            if (cards[key].parentNode !== parent) {
                parent.appendChild(cards[key]);
            }
        }

        function assemble() {
            if (grid.classList.contains('is-regions')) {
                return;
            }
            LEFT.forEach(function (key) {
                move(left, key);
            });
            RIGHT.forEach(function (key) {
                right.insertBefore(cards[key], pair);
            });
            PAIR.forEach(function (key) {
                move(pair, key);
            });
            grid.classList.add('is-regions');
        }

        function flatten() {
            if (!grid.classList.contains('is-regions')) {
                return;
            }
            ORDER.forEach(function (key) {
                move(grid, key);
            });
            grid.classList.remove('is-regions');
        }

        function apply() {
            if (media.matches) {
                assemble();
            } else {
                flatten();
            }
            syncTopPair();
        }

        // B10.2: Saldo e Visão financeira ficam no topo de regiões diferentes.
        // A região mais alta define a altura; o card menor recebe min-height.
        // A regra vive em uma folha construída (CSSOM), porque o CSP do projeto
        // proíbe estilo inline (`style-src-attr 'none'`).
        var topPairSheet = null;

        function setTopPairRule(selector, height) {
            if (!selector) {
                if (topPairSheet) {
                    document.adoptedStyleSheets = document.adoptedStyleSheets.filter(function (sheet) {
                        return sheet !== topPairSheet;
                    });
                    topPairSheet = null;
                }
                return;
            }
            if (!topPairSheet) {
                if (typeof CSSStyleSheet !== 'function' || !document.adoptedStyleSheets) {
                    return;
                }
                topPairSheet = new CSSStyleSheet();
                document.adoptedStyleSheets = document.adoptedStyleSheets.concat(topPairSheet);
            }
            topPairSheet.replaceSync(selector + '{min-height:' + height + 'px}');
        }

        function syncTopPair() {
            var hero = cards.balance;
            var finance = cards.finance;
            if (!hero || !finance) {
                return;
            }
            if (!grid.classList.contains('is-regions')) {
                setTopPairRule(null, 0);
                return;
            }
            // Se já estão iguais, não limpa nada: evita o loop com o observer.
            if (Math.abs(hero.getBoundingClientRect().height - finance.getBoundingClientRect().height) <= 1) {
                return;
            }
            setTopPairRule(null, 0);
            var heroHeight = hero.getBoundingClientRect().height;
            var financeHeight = finance.getBoundingClientRect().height;
            var tallest = Math.max(heroHeight, financeHeight);
            if (tallest - Math.min(heroHeight, financeHeight) <= 1) {
                return;
            }
            var target = heroHeight < financeHeight ? '#dashboard-content.is-regions #cbp-hero' : '#dashboard-content.is-regions #cbp-finance';
            setTopPairRule(target, tallest);
        }

        function restoreFocus(el) {
            if (!el || !document.contains(el)) {
                return;
            }
            var active = document.activeElement;
            if (active && active !== document.body && active !== document.documentElement) {
                return;
            }
            try {
                el.focus({ preventScroll: true });
            } catch (error) {
                el.focus();
            }
        }

        function withFocus(fn) {
            var active = document.activeElement;
            var restore = null;
            if (active && active !== document.body && active !== document.documentElement && grid.contains(active)) {
                restore = active;
            } else if ((!active || active === document.body || active === document.documentElement) && lastFocused && grid.contains(lastFocused)) {
                // Alguns navegadores limpam o foco antes de disparar a troca de
                // breakpoint; nesse caso retomamos o último controle do grid.
                restore = lastFocused;
            }
            fn();
            if (!restore) {
                return;
            }
            // O navegador pode limpar o foco depois do movimento: tenta na hora,
            // no próximo frame e após 80ms, sem roubar foco de outro elemento.
            restoreFocus(restore);
            window.requestAnimationFrame(function () {
                restoreFocus(restore);
            });
            window.setTimeout(function () {
                restoreFocus(restore);
            }, 80);
        }

        function onChange() {
            withFocus(apply);
        }

        try {
            withFocus(apply);
        } finally {
            grid.classList.remove('is-layout-pending');
            grid.setAttribute('data-cbp-layout', 'ready');
        }

        var topPairObserver = null;
        if (window.ResizeObserver) {
            topPairObserver = new window.ResizeObserver(function () {
                syncTopPair();
            });
            topPairObserver.observe(cards.balance);
            topPairObserver.observe(cards.finance);
        }
        var onResize = function () {
            window.requestAnimationFrame(syncTopPair);
        };
        window.addEventListener('resize', onResize);
        if (document.fonts && document.fonts.ready) {
            document.fonts.ready.then(syncTopPair);
        }

        var onFocusIn = function (event) {
            if (event.target && event.target !== grid) {
                lastFocused = event.target;
            }
        };
        grid.addEventListener('focusin', onFocusIn);
        var onPointerDown = function (event) {
            if (!grid.contains(event.target)) {
                lastFocused = null;
            }
        };
        document.addEventListener('pointerdown', onPointerDown);

        if (media.addEventListener) {
            media.addEventListener('change', onChange);
        } else if (media.addListener) {
            media.addListener(onChange);
        }
        bound = {
            grid: grid,
            onChange: onChange,
            observer: topPairObserver,
            onFocusIn: onFocusIn,
            onPointerDown: onPointerDown,
            onResize: onResize
        };
    }

    init();

    // Swap substitui o grid; settle pode reescrever a class list. Reavaliar os
    // dois momentos garante a composição de regiões no grid vigente.
    document.addEventListener('htmx:afterSwap', init);
    document.addEventListener('htmx:afterSettle', init);
})();
