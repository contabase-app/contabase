(() => {
    const cardSelector = '[data-faturas-card]';

    let activeRailSection = null;
    let activeRailUpdate = null;
    let activeRailObserver = null;
    let activeRailSelect = null;
    let generation = 0;
    let mutationXHR = null;
    let restoreCardFocus = null;

    // HTMX response headers (including history changes) run after beforeOnLoad.
    // Discard obsolete GETs there, before any nested/OOB region can be touched.
    document.body?.addEventListener('htmx:beforeRequest', (event) => {
        const detail = event.detail || {};
        const config = detail.requestConfig || {};
        const path = String(config.path || '').split('?')[0];
        const verb = String(config.verb || 'get').toLowerCase();
        const read = verb === 'get' && (path === '/faturas' || path === '/faturas/conteudo');
        const mutation = verb !== 'get' && /^\/(cartoes|transacoes)(\/|$)/.test(path);
        if (!document.querySelector('[data-faturas-index]') || (!read && !mutation)) return;
        if (read && config.triggeringEvent?.detail?.invoiceSnapshot) {
            event.preventDefault();
            return;
        }
        if (mutationXHR) {
            event.preventDefault();
            return;
        }
        if (!detail.xhr) return;
        detail.xhr.faturasGeneration = ++generation;
        if (mutation) mutationXHR = detail.xhr;
    });
    document.body?.addEventListener('htmx:beforeOnLoad', (event) => {
        const xhr = event.detail?.xhr;
        if (xhr?.faturasGeneration && xhr.faturasGeneration !== generation) event.preventDefault();
    });
    document.body?.addEventListener('htmx:afterRequest', (event) => {
        if (event.detail?.xhr === mutationXHR) mutationXHR = null;
    });
    document.addEventListener('click', (event) => {
        if (mutationXHR && event.target?.closest?.('[data-faturas-card], [data-month-selector] a, [hx-post], [hx-put], [hx-delete]')) {
            event.preventDefault();
            event.stopImmediatePropagation();
        }
    }, true);
    // Capture before HTMX can queue another submit behind the active request.
    document.addEventListener('submit', (event) => {
        if (mutationXHR) {
            event.preventDefault();
            event.stopImmediatePropagation();
        }
    }, true);

    function updateActiveRail() {
        if (activeRailSection && activeRailSection.isConnected && typeof activeRailUpdate === 'function') {
            activeRailUpdate();
        }
    }

    if (typeof window.addEventListener === 'function') {
        window.addEventListener('resize', updateActiveRail, { passive: true });
    }

    function refreshIcons() {
        if (window.lucide && typeof window.lucide.createIcons === 'function') {
            window.lucide.createIcons();
        }
    }

    function bindRail(section) {
        if (!section || section.dataset.faturasRailBound === 'true') return;
        const rail = section.querySelector('[data-faturas-card-rail]');
        if (!rail) return;
        section.dataset.faturasRailBound = 'true';

        const cards = () => Array.from(section.querySelectorAll(cardSelector));
        const dots = () => Array.from(section.querySelectorAll('[data-faturas-carousel-dots] span'));
        const previous = section.querySelector('[data-faturas-card-prev]');
        const next = section.querySelector('[data-faturas-card-next]');
        let userScrolling = false;
        let scrollTimer;
        let controlsFrame = null;
        let snapSuspended = false;

        const railInset = () => {
            if (typeof window.getComputedStyle !== 'function') return 0;
            const value = parseFloat(window.getComputedStyle(rail).scrollPaddingLeft);
            return Number.isNaN(value) ? 0 : value;
        };
        const railRightInset = () => {
            if (typeof window.getComputedStyle !== 'function') return 0;
            const value = parseFloat(window.getComputedStyle(rail).scrollPaddingRight);
            return Number.isNaN(value) ? railInset() : value;
        };
        const maxScrollLeft = () => Math.max(0, rail.scrollWidth - rail.clientWidth);
        const cardOffset = (card) => {
            return card.getBoundingClientRect().left - rail.getBoundingClientRect().left + rail.scrollLeft - railInset();
        };
        const closestCardIndex = () => {
            const all = cards();
            if (!all.length) return 0;
            const start = rail.getBoundingClientRect().left + railInset();
            return all.reduce((best, card, index) => {
                return Math.abs(card.getBoundingClientRect().left - start) < Math.abs(all[best].getBoundingClientRect().left - start) ? index : best;
            }, 0);
        };
        const syncDotsToIndex = (index) => {
            dots().forEach((dot, dotIndex) => dot.classList.toggle('is-active', dotIndex === index));
        };
        const reduceMotion = () => !!(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
        const suspendSnap = () => {
            if (snapSuspended) return;
            snapSuspended = true;
            rail.classList.add('is-snap-suspended');
        };
        const restoreSnap = () => {
            if (!snapSuspended) return;
            snapSuspended = false;
            rail.classList.remove('is-snap-suspended');
        };
        const scrollToCard = (index) => {
            const all = cards();
            if (!all.length) return;
            const target = all[Math.max(0, Math.min(index, all.length - 1))];
            const left = cardOffset(target);
            rail.scrollTo({
                left: Math.max(0, Math.min(left, maxScrollLeft())),
                behavior: reduceMotion() ? 'auto' : 'smooth'
            });
        };
        const updateControls = () => {
            if (!section.isConnected) return;
            const max = maxScrollLeft();
            const scrollable = max > 2;
            const all = cards();
            const railRect = rail.getBoundingClientRect();
            const firstRect = all[0] ? all[0].getBoundingClientRect() : null;
            const lastRect = all.length ? all[all.length - 1].getBoundingClientRect() : null;
            const atStart = !firstRect || rail.scrollLeft <= 4 || firstRect.left >= railRect.left - 4;
            const atEnd = !lastRect || rail.scrollLeft >= max - 2 || lastRect.right <= railRect.right + 1;
            section.classList.toggle('is-scrollable', scrollable);
            if (previous) previous.disabled = !scrollable || atStart;
            if (next) next.disabled = !scrollable || atEnd;
            // The dots describe the visible rail position, not the selected card.
            if (scrollable) syncDotsToIndex(closestCardIndex());
        };
        const scheduleControlsUpdate = () => {
            if (typeof window.requestAnimationFrame !== 'function') {
                updateControls();
                return;
            }
            if (controlsFrame !== null) return;
            controlsFrame = window.requestAnimationFrame(() => {
                controlsFrame = null;
                updateControls();
            });
        };

        const syncRefreshURL = (card) => {
            const pageURL = new URL(card.href, window.location.origin);
            pageURL.searchParams.set('partial', 'cards');
            const state = document.getElementById('faturas-index-state');
            if (state) {
                state.setAttribute('hx-get', pageURL.pathname + pageURL.search);
                if (typeof window.htmx?.process === 'function') window.htmx.process(state);
            }
            document.querySelectorAll('[data-month-selector] a').forEach((link) => {
                const destination = new URL(link.href, window.location.origin);
                destination.searchParams.set('cartao', card.dataset.faturasCardId);
                link.href = destination.pathname + destination.search;
            });
        };
        const setActive = (card, options = {}) => {
            cards().forEach((item) => {
                const active = item === card;
                item.classList.toggle('is-selected', active);
                if (active) item.setAttribute('aria-current', 'page');
                else item.removeAttribute('aria-current');
            });
            syncDotsToIndex(cards().indexOf(card));
            syncRefreshURL(card);
            if (options.reveal) revealCard(card, { smooth: true });
        };
        const loadCard = (card) => {
            if (mutationXHR) return;
            const url = card.dataset.faturasContentUrl;
            const target = document.getElementById('faturas-index-detail');
            if (!url || !target || !window.htmx) return;
            // One request owns this detail region. A late response for a
            // previously clicked card must not replace the current selection.
            window.htmx.trigger(target, 'htmx:abort');
            setActive(card);
            target.setAttribute('aria-busy', 'true');
            target.inert = true;
            window.htmx.ajax('GET', url, { source: target, target: '#faturas-index-detail', swap: 'innerHTML' })
                .catch(() => { /* HTMX reports failures; replacing a selection intentionally aborts its request. */ });
        };
        const currentCard = () => cards().find((card) => card.classList.contains('is-selected')) || cards()[0];
        const revealCard = (card, options = {}) => {
            if (!card) return;
            userScrolling = false;
            window.clearTimeout(scrollTimer);
            const railRect = rail.getBoundingClientRect();
            const cardRect = card.getBoundingClientRect();
            const visibleLeft = railRect.left + railInset();
            const visibleRight = railRect.right - railRightInset();
            let left = rail.scrollLeft;
            if (cardRect.left < visibleLeft) {
                left -= visibleLeft - cardRect.left;
            } else if (cardRect.right > visibleRight) {
                left += cardRect.right - visibleRight;
            }
            left = Math.max(0, Math.min(left, maxScrollLeft()));
            if (Math.abs(left - rail.scrollLeft) < 1) return;
            // Mandatory scroll-snap would pull the rail to a card start and
            // undo a minimal reveal. Suspend it until the next real gesture.
            suspendSnap();
            rail.scrollTo({
                left,
                behavior: options.smooth && !reduceMotion() ? 'smooth' : 'auto'
            });
        };
        const moveRail = (delta) => {
            // Arrow navigation scrolls only. It must never select a card, load
            // an invoice or touch history. Cancelling any pending gesture timer
            // also prevents a previous swipe from promoting the new position.
            restoreSnap();
            userScrolling = false;
            window.clearTimeout(scrollTimer);
            scrollToCard(closestCardIndex() + delta);
        };

        cards().forEach((card) => card.addEventListener('click', (event) => {
            if (mutationXHR) { event.preventDefault(); return; }
            if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button > 0 || !window.htmx) return;
            event.preventDefault();
            revealCard(card, { smooth: true });
            loadCard(card);
        }));
        if (previous && previous.dataset.faturasControlBound !== '1') {
            previous.dataset.faturasControlBound = '1';
            previous.addEventListener('click', () => moveRail(-1));
        }
        if (next && next.dataset.faturasControlBound !== '1') {
            next.dataset.faturasControlBound = '1';
            next.addEventListener('click', () => moveRail(1));
        }
        rail.addEventListener('pointerdown', () => { userScrolling = true; }, { passive: true });
        rail.addEventListener('pointermove', (event) => {
            if (event && event.buttons) restoreSnap();
        }, { passive: true });
        rail.addEventListener('wheel', () => { userScrolling = true; restoreSnap(); }, { passive: true });
        rail.addEventListener('keydown', (event) => {
            if (['ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key)) {
                userScrolling = true;
                restoreSnap();
            }
        });
        rail.addEventListener('scroll', () => {
            scheduleControlsUpdate();
            if (!userScrolling) return;
            window.clearTimeout(scrollTimer);
            scrollTimer = window.setTimeout(() => {
                if (!section.isConnected) return;
                userScrolling = false;
                const all = cards();
                if (!all.length) return;
                const railLeft = rail.getBoundingClientRect().left;
                const atEnd = rail.scrollLeft > 0 && rail.scrollLeft >= rail.scrollWidth - rail.clientWidth - 2;
                const nearest = atEnd ? all[all.length - 1] : all.reduce((best, card) =>
                    Math.abs(card.getBoundingClientRect().left - railLeft) < Math.abs(best.getBoundingClientRect().left - railLeft) ? card : best
                );
                if (nearest !== currentCard()) loadCard(nearest);
            }, 180);
        }, { passive: true });
        const selected = currentCard();
        if (selected) {
            setActive(selected);
            revealCard(selected);
            if (window.location.pathname === '/faturas' && window.history?.replaceState) {
                const current = new URL(window.location.href);
                if (!current.searchParams.has('mes') && !current.searchParams.has('ano')) {
                    window.history.replaceState(window.history.state, '', selected.href);
                }
            }
        }

        activeRailSection = section;
        activeRailUpdate = updateControls;
        activeRailSelect = setActive;
        if (activeRailObserver) {
            activeRailObserver.disconnect();
            activeRailObserver = null;
        }
        if (typeof window.ResizeObserver === 'function') {
            const observer = new window.ResizeObserver(() => {
                if (!section.isConnected) {
                    observer.disconnect();
                    return;
                }
                updateControls();
            });
            activeRailObserver = observer;
            observer.observe(rail);
        }
        updateControls();
        if (typeof window.requestAnimationFrame === 'function') {
            window.requestAnimationFrame(updateControls);
        }
    }

    function bindAll() {
        document.querySelectorAll('#faturas-index-card-rail').forEach(bindRail);
        if (restoreCardFocus) {
            const cardID = restoreCardFocus;
            restoreCardFocus = null;
            // HTMX restores element IDs after its OOB swaps. Restore the card
            // afterwards so a removed payment form cannot take focus back.
            const restore = () => {
                const active = document.activeElement;
                if (active && active !== document.body && !active.closest?.('#faturas-index-state')) return;
                const card = Array.from(document.querySelectorAll(cardSelector)).find((item) => item.dataset.faturasCardId === cardID);
                card?.focus({ preventScroll: true });
            };
            if (typeof window.requestAnimationFrame === 'function') window.requestAnimationFrame(restore);
            else restore();
        }
        refreshIcons();
    }

    const rememberFocus = (event) => {
        const target = event.detail?.target;
        if (target?.id !== 'faturas-index-state') return;
        const active = document.activeElement;
        const card = active?.closest?.(cardSelector);
        if (card) restoreCardFocus = card.dataset.faturasCardId;
        else if (active?.closest?.('#invoice-summary, #bottom-sheet-container')) {
            restoreCardFocus = document.querySelector('[data-faturas-card].is-selected')?.dataset.faturasCardId;
        }
    };
    document.body?.addEventListener('htmx:beforeSwap', rememberFocus);
    document.body?.addEventListener('htmx:oobBeforeSwap', rememberFocus);
    document.body?.addEventListener('htmx:oobAfterSwap', bindAll);

    document.addEventListener('DOMContentLoaded', bindAll);
    document.body?.addEventListener('htmx:afterSwap', (event) => {
        if (event.target?.id === 'faturas-index-detail') {
            event.target.removeAttribute('aria-busy');
            event.target.inert = false;
        }
        bindAll();
    });
    document.body?.addEventListener('htmx:afterRequest', (event) => {
        const detail = event.detail || {};
        if (detail.xhr?.faturasGeneration && detail.xhr.faturasGeneration !== generation) return;
        if (detail.elt?.id === 'faturas-index-detail') {
            detail.elt.removeAttribute('aria-busy');
            detail.elt.inert = false;
            if (detail.failed && window.location.href) {
                const selectedID = new URL(window.location.href).searchParams.get('cartao');
                const card = Array.from(document.querySelectorAll(cardSelector)).find((item) => item.dataset.faturasCardId === selectedID);
                if (card && activeRailSelect) activeRailSelect(card, { reveal: true });
            }
        }
    });

    document.body?.addEventListener('htmx:historyRestore', (event) => {
        const syncSelection = () => {
            const restored = event.detail?.path || window.location.href;
            const cardID = new URL(restored, window.location.origin).searchParams.get('cartao');
            if (!cardID) return;
            const card = Array.from(document.querySelectorAll(cardSelector)).find((item) => item.dataset.faturasCardId === cardID);
            if (card && activeRailSelect) activeRailSelect(card, { reveal: true });
        };
        if (typeof window.requestAnimationFrame === 'function') window.requestAnimationFrame(syncSelection);
        else syncSelection();
    });
})();
