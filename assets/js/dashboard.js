(function () {
    'use strict';

    function initActionTabs(root) {
        var tabRoot = root.querySelector('[data-v2-action-tabs]');
        if (!tabRoot || tabRoot.dataset.v2TabsReady === 'true') return;
        tabRoot.dataset.v2TabsReady = 'true';

        var tabs = Array.prototype.slice.call(tabRoot.querySelectorAll('[data-v2-action-tab]'));
        var panels = tabRoot.querySelectorAll('[data-v2-action-panel]');
        var selectTab = function (selectedTab) {
            var selected = selectedTab.getAttribute('data-v2-action-tab');
            tabs.forEach(function (item) {
                var isSelected = item === selectedTab;
                item.setAttribute('aria-selected', String(isSelected));
                item.setAttribute('tabindex', isSelected ? '0' : '-1');
            });
            panels.forEach(function (panel) {
                panel.hidden = panel.getAttribute('data-v2-action-panel') !== selected;
            });
        };
        tabs.forEach(function (tab, index) {
            tab.addEventListener('click', function () {
                selectTab(tab);
            });
            tab.addEventListener('keydown', function (event) {
                if (event.key !== 'ArrowRight' && event.key !== 'ArrowLeft' && event.key !== 'Home' && event.key !== 'End') return;
                event.preventDefault();
                var nextIndex = index;
                if (event.key === 'ArrowRight') nextIndex = (index + 1) % tabs.length;
                if (event.key === 'ArrowLeft') nextIndex = (index - 1 + tabs.length) % tabs.length;
                if (event.key === 'Home') nextIndex = 0;
                if (event.key === 'End') nextIndex = tabs.length - 1;
                tabs[nextIndex].focus();
                selectTab(tabs[nextIndex]);
            });
        });
        if (tabs.length) {
            selectTab(tabs.find(function (tab) { return tab.getAttribute('aria-selected') === 'true'; }) || tabs[0]);
        }
    }

    var lastCardIndex = 0;

    function initCarousel(root) {
        var rail = root.querySelector('[data-v2-carousel]');
        if (!rail || rail.dataset.v2CarouselReady === 'true') return;
        rail.dataset.v2CarouselReady = 'true';
        var cards = Array.from(rail.querySelectorAll('[data-wallet-card]'));
        // The add-card placeholder scrolls with the rail but never counts as a
        // real card in the position label or in financial metrics.
        var slides = Array.from(rail.querySelectorAll('[data-wallet-card], [data-wallet-add-card]'));
        var cardSlides = cards.map(function (card) { return slides.indexOf(card); });
        var previous = root.querySelector('[data-v2-carousel-prev]');
        var next = root.querySelector('[data-v2-carousel-next]');
        var position = root.querySelector('[data-wallet-position]');
        if (!cards.length || !previous || !next || !position) return;
        var reduceMotion = function () { return window.matchMedia('(prefers-reduced-motion: reduce)').matches; };

        function railInset() {
            var value = parseFloat(window.getComputedStyle(rail).scrollPaddingLeft);
            return isNaN(value) ? 0 : value;
        }
        function railStart() { return rail.getBoundingClientRect().left + railInset(); }
        function railEnd() {
            var inset = parseFloat(window.getComputedStyle(rail).scrollPaddingRight);
            return rail.getBoundingClientRect().right - (isNaN(inset) ? railInset() : inset);
        }
        function slideScrollLeft(index) {
            return slides[index].getBoundingClientRect().left - rail.getBoundingClientRect().left + rail.scrollLeft - railInset();
        }
        function closestIndex() {
            var start = railStart();
            return slides.reduce(function (best, slide, index) {
                return Math.abs(slide.getBoundingClientRect().left - start) < Math.abs(slides[best].getBoundingClientRect().left - start) ? index : best;
            }, 0);
        }
        function closestCardIndex() {
            var start = railStart();
            return cards.reduce(function (best, card, index) {
                return Math.abs(card.getBoundingClientRect().left - start) < Math.abs(cards[best].getBoundingClientRect().left - start) ? index : best;
            }, 0);
        }
        function visibleCardRange() {
            var start = railStart();
            var end = railEnd();
            var first = -1;
            var last = -1;
            slides.forEach(function (slide, index) {
                if (!slide.hasAttribute('data-wallet-card')) return;
                var rect = slide.getBoundingClientRect();
                if (rect.left >= start - 1 && rect.right <= end + 1) {
                    var cardIndex = cardSlides.indexOf(index);
                    if (cardIndex < 0) return;
                    if (first < 0) first = cardIndex;
                    last = cardIndex;
                }
            });
            if (first < 0) {
                var closest = closestCardIndex();
                return [closest, closest];
            }
            return [first, last];
        }
        function scrollToIndex(index, behavior) {
            rail.scrollTo({ left: slideScrollLeft(index), behavior: behavior });
        }
        // Arrows reflect the real scroll range: the add-card slide is the end.
        function update() {
            var max = rail.scrollWidth - rail.clientWidth;
            previous.disabled = rail.scrollLeft <= 1;
            next.disabled = rail.scrollLeft >= max - 1;
            if (max <= 1) {
                lastCardIndex = 0;
                position.textContent = cards.length === 1 ? '1 cartão' : cards.length + ' cartões';
                return;
            }
            var range = visibleCardRange();
            lastCardIndex = range[0];
            position.textContent = range[1] > range[0]
                ? 'Cartões ' + (range[0] + 1) + '–' + (range[1] + 1) + ' de ' + cards.length
                : 'Cartão ' + (range[0] + 1) + ' de ' + cards.length;
        }

        previous.addEventListener('click', function () { scrollToIndex(Math.max(0, closestIndex() - 1), 'smooth'); });
        next.addEventListener('click', function () { scrollToIndex(Math.min(slides.length - 1, closestIndex() + 1), 'smooth'); });
        cards.forEach(function (card, index) {
            card.addEventListener('keydown', function (event) {
                var target;
                var slideIndex = cardSlides[index];
                if (event.key === 'ArrowRight') target = Math.min(slideIndex + 1, slides.length - 1);
                if (event.key === 'ArrowLeft') target = Math.max(slideIndex - 1, 0);
                if (event.key === 'Home') target = 0;
                if (event.key === 'End') target = slides.length - 1;
                if (target === undefined) return;
                event.preventDefault();
                slides[target].focus({ preventScroll: true });
                scrollToIndex(target, reduceMotion() ? 'auto' : 'smooth');
            });
        });
        var scrollTimer;
        rail.addEventListener('scroll', function () {
            clearTimeout(scrollTimer);
            scrollTimer = setTimeout(function () {
                if (rail.isConnected) update();
            }, 120);
        }, { passive: true });
        update();
        // Restore the visible card after an HTMX replacement without animation.
        if (lastCardIndex) rail.scrollLeft = slideScrollLeft(lastCardIndex);
    }

    function initialize() {
        var root = document.querySelector('#dashboard-content.wallet');
        if (!root) return;
        initActionTabs(root);
        if (typeof window.applyCardSchemes === 'function') window.applyCardSchemes(root);
        initCarousel(root);
        if (typeof window.initPrivacyToggle === 'function') window.initPrivacyToggle(root);
        if (window.lucide && typeof window.lucide.createIcons === 'function') {
            window.lucide.createIcons();
        }
    }

    document.addEventListener('htmx:afterSwap', initialize);
    document.addEventListener('htmx:oobAfterSwap', initialize);
    window.addEventListener('resize', function () {
        var rail = document.querySelector('.wallet [data-v2-carousel]');
        if (!rail) return;
        var slides = rail.querySelectorAll('[data-wallet-card], [data-wallet-add-card]');
        if (slides[lastCardIndex]) {
            var inset = parseFloat(window.getComputedStyle(rail).scrollPaddingLeft) || 0;
            rail.scrollLeft = slides[lastCardIndex].getBoundingClientRect().left - rail.getBoundingClientRect().left + rail.scrollLeft - inset;
        }
        rail.dispatchEvent(new Event('scroll'));
    });

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        initialize();
    }
}());
