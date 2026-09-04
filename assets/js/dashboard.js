(function () {
    'use strict';

    function initActionTabs(root) {
        var tabRoot = root.querySelector('[data-v2-action-tabs]');
        if (!tabRoot || tabRoot.dataset.v2TabsReady === 'true') return;
        tabRoot.dataset.v2TabsReady = 'true';

        var tabs = tabRoot.querySelectorAll('[data-v2-action-tab]');
        var panels = tabRoot.querySelectorAll('[data-v2-action-panel]');
        tabs.forEach(function (tab) {
            tab.addEventListener('click', function () {
                var selected = tab.getAttribute('data-v2-action-tab');
                tabs.forEach(function (item) {
                    item.setAttribute('aria-selected', String(item === tab));
                });
                panels.forEach(function (panel) {
                    panel.hidden = panel.getAttribute('data-v2-action-panel') !== selected;
                });
            });
        });
    }

    function initCarousel(root) {
        var rail = root.querySelector('[data-v2-carousel]');
        var dots = root.querySelectorAll('[data-v2-carousel-dots] span');
        var previous = root.querySelector('[data-v2-carousel-prev]');
        var next = root.querySelector('[data-v2-carousel-next]');
        if (!rail || rail.dataset.v2CarouselReady === 'true') return;
        rail.dataset.v2CarouselReady = 'true';

        var cardStep = function () {
            var firstCard = rail.querySelector('.cb-v2-credit-card');
            var styles = window.getComputedStyle(rail);
            var gap = parseFloat(styles.columnGap || styles.gap || '0') || 12;
            return firstCard ? firstCard.getBoundingClientRect().width + gap : rail.clientWidth * 0.8;
        };

        var updateDots = function () {
            var cards = rail.children;
            if (!cards.length || !dots.length) return;
            var center = rail.scrollLeft + rail.clientWidth / 2;
            var activeIndex = 0;
            var closestDistance = Infinity;
            for (var index = 0; index < cards.length; index += 1) {
                var cardCenter = cards[index].offsetLeft + cards[index].offsetWidth / 2;
                var distance = Math.abs(center - cardCenter);
                if (distance < closestDistance) {
                    closestDistance = distance;
                    activeIndex = index;
                }
            }
            dots.forEach(function (dot, index) {
                dot.classList.toggle('is-active', index === activeIndex);
            });
        };

        var updateControls = function () {
            var maxScroll = Math.max(0, rail.scrollWidth - rail.clientWidth - 1);
            var canScroll = maxScroll > 1;
            if (previous) previous.disabled = !canScroll || rail.scrollLeft <= 4;
            if (next) next.disabled = !canScroll || rail.scrollLeft >= maxScroll - 4;
        };

        if (previous) {
            previous.addEventListener('click', function () {
                rail.scrollBy({ left: -cardStep(), behavior: 'smooth' });
            });
        }

        if (next) {
            next.addEventListener('click', function () {
                rail.scrollBy({ left: cardStep(), behavior: 'smooth' });
            });
        }

        var frame = null;
        rail.addEventListener('scroll', function () {
            if (frame) return;
            frame = window.requestAnimationFrame(function () {
                frame = null;
                updateDots();
                updateControls();
            });
        }, { passive: true });
        updateDots();
        updateControls();
        window.addEventListener('resize', function () {
            updateDots();
            updateControls();
        });
    }

    function initMobileCardDisclosure(root) {
        var group = root.querySelector('[data-v2-card-group]');
        if (!group || group.dataset.v2CardDisclosureReady === 'true') return;
        group.dataset.v2CardDisclosureReady = 'true';

        var initial = parseInt(group.getAttribute('data-v2-show-more-initial'), 10) || 6;
        var cards = group.querySelectorAll('[data-v2-card-item]');
        var toggle = group.querySelector('[data-v2-mobile-cards-toggle]');
        var label = group.querySelector('[data-v2-show-more-label]');
        var media = window.matchMedia('(max-width: 767px)');
        var expanded = false;

        var update = function () {
            var shouldCollapse = media.matches && !expanded;
            cards.forEach(function (card, index) {
                card.hidden = shouldCollapse && index >= initial;
            });

            if (toggle) {
                toggle.hidden = !media.matches || cards.length <= initial;
                toggle.setAttribute('aria-expanded', String(media.matches && expanded));
            }
            if (label) {
                label.textContent = expanded ? 'Mostrar menos' : 'Mostrar mais (mais ' + Math.max(0, cards.length - initial) + ')';
            }
        };

        if (toggle) {
            toggle.addEventListener('click', function () {
                expanded = !expanded;
                update();
            });
        }

        if (typeof media.addEventListener === 'function') {
            media.addEventListener('change', update);
        } else {
            media.addListener(update);
        }
        update();
    }

    function initialize() {
        var root = document.querySelector('[data-dashboard]');
        if (!root) return;
        initActionTabs(root);
        initCarousel(root);
        initMobileCardDisclosure(root);
        if (window.lucide && typeof window.lucide.createIcons === 'function') {
            window.lucide.createIcons();
        }
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        initialize();
    }
}());
