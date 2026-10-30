(() => {
    const cardSelector = '[data-faturas-card]';

    let activeRailSection = null;
    let activeRailUpdate = null;
    let activeRailObserver = null;

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
            section.setAttribute('hx-get', pageURL.pathname + pageURL.search);
            document.querySelectorAll('[data-month-selector] a').forEach((link) => {
                const destination = new URL(link.href, window.location.origin);
                destination.searchParams.set('cartao', card.dataset.faturasCardId);
                link.href = destination.pathname + destination.search;
            });
        };
        const setActive = (card) => {
            cards().forEach((item) => {
                const active = item === card;
                item.classList.toggle('is-selected', active);
                if (active) item.setAttribute('aria-current', 'page');
                else item.removeAttribute('aria-current');
            });
            syncDotsToIndex(cards().indexOf(card));
            syncRefreshURL(card);
        };
        const loadCard = (card) => {
            const url = card.dataset.faturasContentUrl;
            const target = document.getElementById('faturas-index-detail');
            if (!url || !target || !window.htmx) return;
            // One request owns this detail region. A late response for a
            // previously clicked card must not replace the current selection.
            window.htmx.trigger(target, 'htmx:abort');
            setActive(card);
            target.setAttribute('aria-busy', 'true');
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
        }

        activeRailSection = section;
        activeRailUpdate = updateControls;
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
        refreshIcons();
    }

    document.addEventListener('DOMContentLoaded', bindAll);
    document.body?.addEventListener('htmx:afterSwap', (event) => {
        if (event.target?.id === 'faturas-index-detail') event.target.removeAttribute('aria-busy');
        bindAll();
    });
    document.body?.addEventListener('htmx:afterRequest', (event) => {
        if (event.detail?.elt?.id === 'faturas-index-detail') event.detail.elt.removeAttribute('aria-busy');
    });
})();
