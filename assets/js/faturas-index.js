(() => {
    const cardSelector = '[data-faturas-card]';

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
        let userScrolling = false;
        let scrollTimer;
        const syncRefreshURL = (card) => {
            const pageURL = new URL(card.href, window.location.origin);
            pageURL.searchParams.set('partial', 'cards');
            section.setAttribute('hx-get', pageURL.pathname + pageURL.search);
            document.querySelectorAll('.cb-faturas-index-period a').forEach((link) => {
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
            dots().forEach((dot, index) => dot.classList.toggle('is-active', cards()[index] === card));
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
        const revealCard = (card) => {
            if (!card) return;
            userScrolling = false;
            window.clearTimeout(scrollTimer);
            // Move only the horizontal rail; page scrolling and scroll-snap
            // events must never override a card explicitly selected by URL.
            const left = rail.scrollLeft + card.getBoundingClientRect().left - rail.getBoundingClientRect().left;
            rail.scrollTo({ left: Math.max(0, Math.min(left, rail.scrollWidth - rail.clientWidth)), behavior: 'instant' });
        };

        cards().forEach((card) => card.addEventListener('click', (event) => {
            if (event.ctrlKey || event.metaKey || event.shiftKey || event.altKey || event.button > 0 || !window.htmx) return;
            event.preventDefault();
            revealCard(card);
            loadCard(card);
        }));
        section.querySelector('[data-faturas-card-prev]')?.addEventListener('click', () => {
            const all = cards();
            const index = Math.max(0, all.indexOf(currentCard()) - 1);
            revealCard(all[index]);
            if (all[index]) loadCard(all[index]);
        });
        section.querySelector('[data-faturas-card-next]')?.addEventListener('click', () => {
            const all = cards();
            const index = Math.min(all.length - 1, all.indexOf(currentCard()) + 1);
            revealCard(all[index]);
            if (all[index]) loadCard(all[index]);
        });
        rail.addEventListener('pointerdown', () => { userScrolling = true; }, { passive: true });
        rail.addEventListener('wheel', () => { userScrolling = true; }, { passive: true });
        rail.addEventListener('keydown', (event) => {
            if (['ArrowLeft', 'ArrowRight', 'Home', 'End', 'PageUp', 'PageDown'].includes(event.key)) userScrolling = true;
        });
        rail.addEventListener('scroll', () => {
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
    }

    function bindAll() {
        document.querySelectorAll('#faturas-index-card-rail').forEach(bindRail);
        refreshIcons();
    }

    function centerSelectedMonth() {
        const months = document.querySelector('.cb-faturas-index-months');
        const selected = months?.querySelector('.cb-faturas-index-month.is-active');
        if (!months || !selected || window.matchMedia('(min-width: 768px)').matches) return;
        const offset = selected.offsetLeft - ((months.clientWidth - selected.offsetWidth) / 2);
        months.scrollLeft = Math.max(0, offset);
    }

    document.addEventListener('DOMContentLoaded', () => {
        bindAll();
        centerSelectedMonth();
    });
    document.body?.addEventListener('htmx:afterSwap', (event) => {
        if (event.target?.id === 'faturas-index-detail') event.target.removeAttribute('aria-busy');
        bindAll();
    });
    document.body?.addEventListener('htmx:afterRequest', (event) => {
        if (event.detail?.elt?.id === 'faturas-index-detail') event.detail.elt.removeAttribute('aria-busy');
    });
})();
