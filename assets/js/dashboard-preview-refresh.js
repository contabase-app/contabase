/*
 * Dashboard V2 — contrato canônico de atualização HTMX/OOB (R1.3-D3-B1).
 *
 * O Dashboard V2 não recebe markup de Dashboard por OOB. Toda mutação financeira
 * emite o evento `refreshFinancials`; `#dashboard-content` escuta o evento e se
 * atualiza pelo próprio endpoint do V2 com `hx-select="#dashboard-content"`.
 *
 * Este script cobre o que o HTML declarativo não resolve:
 * 1. a URL do refresh acompanha a URL viva — mês e faixa do fluxo podem mudar por
 *    hx-push-url sem re-renderizar o grid, e o refresh não pode revertê-los;
 * 2. foco e rolagem sobrevivem à troca `outerHTML` do grid, mesmo quando outro
 *    script (layout responsivo, ícones) reorganiza o DOM logo depois;
 * 3. não há refresh concorrente duplicado: `hx-sync="this:replace"` no grid
 *    substitui a requisição em andamento em vez de acumular listeners.
 *
 * Não altera valores, filtros do servidor, autorização ou isolamento.
 */
(function () {
    'use strict';

    var GRID_ID = 'dashboard-content';
    var REFRESH_ATTR = 'data-cbp-refresh-root';
    var pendingFocus = null;
    var restoreTimers = [];
    var userInteracted = false;

    function isRefreshRoot(el) {
        return !!(el && el.id === GRID_ID && el.hasAttribute(REFRESH_ATTR));
    }

    function currentGrid() {
        return document.getElementById(GRID_ID);
    }

    // hx-get carrega a URL canônica do SSR. Quando o usuário muda mês/faixa via
    // hx-push-url, a URL viva é a fonte da verdade para o próximo refresh.
    function syncRefreshPath(event) {
        var detail = event && event.detail;
        if (!detail || !isRefreshRoot(detail.elt)) {
            return;
        }
        var path = window.location.pathname + window.location.search;
        if (path) {
            detail.path = path;
        }
    }

    // Month links live outside the swapped flow card and must follow its range.
    function syncMonthLinks() {
        if (!currentGrid()) return;
        var range = new URLSearchParams(window.location.search).get('fluxo');
        var links = document.documentElement.querySelectorAll('[data-cbp-month-link]');
        for (var i = 0; i < links.length; i++) {
            var href = links[i].getAttribute('href');
            var parts = href.split('?');
            var params = new URLSearchParams(parts[1] || '');
            if (range && range !== '6m') {
                params.set('fluxo', range);
            } else {
                params.delete('fluxo');
            }
            var query = params.toString();
            links[i].setAttribute('href', parts[0] + (query ? '?' + query : ''));
        }
    }

    function focusDescriptor(el) {
        if (!el || el === document.body || el === document.documentElement) {
            return null;
        }
        var section = el.closest ? el.closest('[data-cbp-section]') : null;
        return {
            id: el.id || '',
            section: section ? section.getAttribute('data-cbp-section') : '',
            tag: (el.tagName || '').toLowerCase(),
            controls: el.getAttribute('aria-controls') || '',
            label: (el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 80),
            scrollX: window.scrollX || 0,
            scrollY: window.scrollY || 0,
            scrolled: false
        };
    }

    function focusWasLost() {
        var active = document.activeElement;
        return !active || active === document.body || active === document.documentElement || !document.contains(active);
    }

    function clearTimers() {
        for (var i = 0; i < restoreTimers.length; i++) {
            window.clearTimeout(restoreTimers[i]);
        }
        restoreTimers = [];
    }

    function captureFocus(event) {
        var detail = event && event.detail;
        if (!detail || !detail.target || detail.target.id !== GRID_ID) {
            return;
        }
        clearTimers();
        userInteracted = false;
        pendingFocus = null;
        var active = document.activeElement;
        if (active && active !== detail.target && detail.target.contains(active)) {
            pendingFocus = focusDescriptor(active);
        }
    }

    function findByDescriptor(grid, descriptor) {
        if (descriptor.id) {
            var byId = document.getElementById(descriptor.id);
            if (byId && grid.contains(byId)) {
                return byId;
            }
        }
        var scope = grid;
        if (descriptor.section) {
            scope = grid.querySelector('[data-cbp-section="' + descriptor.section + '"]') || grid;
        }
        var candidates = scope.querySelectorAll(descriptor.tag || 'button, a, [tabindex]');
        for (var i = 0; i < candidates.length; i++) {
            var el = candidates[i];
            if (descriptor.controls && el.getAttribute('aria-controls') === descriptor.controls) {
                return el;
            }
            if (!descriptor.controls) {
                var label = (el.getAttribute('aria-label') || el.textContent || '').trim().slice(0, 80);
                if (label && label === descriptor.label) {
                    return el;
                }
            }
        }
        return null;
    }

    function attemptRestore() {
        var descriptor = pendingFocus;
        if (!descriptor) {
            return;
        }
        var grid = currentGrid();
        if (!grid) {
            return;
        }
        // Nunca roubar foco de um controle válido (usuário ou HTMX por id).
        if (!focusWasLost()) {
            return;
        }
        if (!descriptor.scrolled) {
            descriptor.scrolled = true;
            if (!userInteracted && Math.abs((window.scrollY || 0) - descriptor.scrollY) > 24 && typeof window.scrollTo === 'function') {
                window.scrollTo(descriptor.scrollX, descriptor.scrollY);
            }
        }
        var target = findByDescriptor(grid, descriptor);
        if (!target || typeof target.focus !== 'function') {
            return;
        }
        try {
            target.focus({ preventScroll: true });
        } catch (error) {
            target.focus();
        }
    }

    function scheduleRestore() {
        if (!pendingFocus) {
            return;
        }
        attemptRestore();
        // Layout responsivo, ícones e sincronização de workspace reorganizam o
        // DOM logo após o swap. Reinsistimos até a poeira baixar, sem roubar foco
        // de um controle que o usuário escolheu nesse intervalo.
        [40, 120, 240, 360].forEach(function (delay, index, all) {
            restoreTimers.push(window.setTimeout(function () {
                if (!pendingFocus) {
                    return;
                }
                if (userInteracted) {
                    pendingFocus = null;
                    clearTimers();
                    return;
                }
                attemptRestore();
                if (index === all.length - 1) {
                    pendingFocus = null;
                    clearTimers();
                }
            }, delay));
        });
    }

    // A resposta HTMX pode trazer o gate anti-flash do primeiro paint. Um
    // refresh nunca pode esconder o grid por 1,5s, então removemos o gate assim
    // que o swap estabiliza (o layout responsivo já montou as regiões).
    function revealGrid() {
        var grid = currentGrid();
        if (grid) {
            grid.classList.remove('is-layout-pending');
        }
    }

    function markInteraction() {
        if (pendingFocus) {
            userInteracted = true;
        }
    }

    document.addEventListener('htmx:configRequest', syncRefreshPath);
    document.addEventListener('htmx:pushedIntoHistory', syncMonthLinks);
    document.addEventListener('htmx:beforeSwap', captureFocus);
    document.addEventListener('htmx:afterSwap', scheduleRestore);
    document.addEventListener('htmx:afterSettle', revealGrid);
    document.addEventListener('pointerdown', markInteraction, true);
    document.addEventListener('wheel', markInteraction, { capture: true, passive: true });
    document.addEventListener('keydown', markInteraction, true);
}());
