(function () {
    'use strict';

    var flowExpanded = false;

    function flowMobile() {
        return typeof window.matchMedia === 'function' && window.matchMedia('(max-width: 1023px)').matches;
    }

    function initFlowCollapse(root) {
        var card = root.querySelector('[data-cbp-flow-root]');
        if (!card || card.dataset.cbpFlowCollapseReady === 'true') return;
        var toggle = card.querySelector('[data-cbp-flow-toggle]');
        var panel = card.querySelector('[data-cbp-flow-panel]');
        if (!toggle || !panel) return;
        var text = toggle.querySelector('[data-cbp-flow-toggle-text]');
        card.dataset.cbpFlowCollapseReady = 'true';

        function apply() {
            if (!flowMobile()) {
                panel.hidden = false;
                toggle.setAttribute('aria-expanded', 'true');
                if (text) text.textContent = 'Recolher gráfico';
                return;
            }
            panel.hidden = !flowExpanded;
            toggle.setAttribute('aria-expanded', String(flowExpanded));
            if (text) text.textContent = flowExpanded ? 'Recolher gráfico' : 'Ver gráfico';
        }

        toggle.addEventListener('click', function () {
            flowExpanded = !flowExpanded;
            apply();
        });
        card.__cbpFlowApply = apply;
        apply();
    }

    function initFocusCards(root) {
        var cards = root.querySelectorAll('[data-cbp-focus-card]');
        Array.prototype.forEach.call(cards, function (card) {
            if (card.dataset.cbpFocusReady === 'true') return;
            var toggle = card.querySelector('[data-cbp-focus-toggle]');
            var content = card.querySelector('[data-cbp-focus-content]');
            var overlay = card.querySelector('[data-cbp-focus-overlay]');
            if (!toggle || !content || !overlay) return;
            var text = toggle.querySelector('[data-cbp-focus-toggle-text]');
            card.dataset.cbpFocusReady = 'true';

            function apply(revealed) {
                if (revealed) {
                    content.removeAttribute('inert');
                    content.removeAttribute('aria-hidden');
                } else {
                    content.setAttribute('inert', '');
                    content.setAttribute('aria-hidden', 'true');
                }
                overlay.hidden = revealed;
                toggle.setAttribute('aria-expanded', String(revealed));
                if (text) text.textContent = revealed ? 'Ocultar dados atuais' : 'Ver dados atuais';
                card.classList.toggle('is-focus-revealed', revealed);
            }

            toggle.addEventListener('click', function () {
                apply(toggle.getAttribute('aria-expanded') !== 'true');
            });
            apply(false);
        });
    }

    function initFinanceTabs(root) {
        var tablists = root.querySelectorAll('[data-cbp-finance-tabs]');
        Array.prototype.forEach.call(tablists, function (tablist) {
            if (tablist.dataset.cbpFinanceTabsReady === 'true') return;
            var tabs = Array.prototype.slice.call(tablist.querySelectorAll('[role="tab"]'));
            if (!tabs.length) return;
            tablist.dataset.cbpFinanceTabsReady = 'true';

            function panelFor(tab) {
                var id = tab.getAttribute('aria-controls');
                return id ? document.getElementById(id) : null;
            }

            function activate(selectedTab, moveFocus) {
                tabs.forEach(function (tab) {
                    var selected = tab === selectedTab;
                    tab.setAttribute('aria-selected', String(selected));
                    tab.tabIndex = selected ? 0 : -1;
                    tab.classList.toggle('is-active', selected);
                    var panel = panelFor(tab);
                    if (panel) panel.hidden = !selected;
                });
                if (moveFocus) selectedTab.focus();
            }

            tabs.forEach(function (tab, index) {
                tab.addEventListener('click', function () {
                    activate(tab, false);
                });
                tab.addEventListener('keydown', function (event) {
                    var nextIndex = null;
                    if (event.key === 'ArrowRight') {
                        nextIndex = (index + 1) % tabs.length;
                    } else if (event.key === 'ArrowLeft') {
                        nextIndex = (index - 1 + tabs.length) % tabs.length;
                    } else if (event.key === 'Home') {
                        nextIndex = 0;
                    } else if (event.key === 'End') {
                        nextIndex = tabs.length - 1;
                    }
                    if (nextIndex === null) return;
                    event.preventDefault();
                    activate(tabs[nextIndex], true);
                });
            });

            var initial = null;
            tabs.forEach(function (tab) {
                if (!initial && tab.getAttribute('aria-selected') === 'true') initial = tab;
            });
            activate(initial || tabs[0], false);
        });
    }

    function initFlow(root) {
        var plot = root.querySelector('[data-cbp-flow-plot]');
        if (!plot || plot.dataset.cbpFlowReady === 'true') return;
        var svg = plot.querySelector('.cbp-flow-svg');
        var tooltip = plot.querySelector('[data-flow-tooltip]');
        if (!svg || !tooltip) return;
        var groups = Array.prototype.slice.call(svg.querySelectorAll('[data-flow-index]'));
        if (!groups.length) return;

        var label = tooltip.querySelector('[data-flow-tooltip-label]');
        var income = tooltip.querySelector('[data-flow-tooltip-income]');
        var expense = tooltip.querySelector('[data-flow-tooltip-expense]');
        var net = tooltip.querySelector('[data-flow-tooltip-net]');
        var netLabel = tooltip.querySelector('[data-flow-tooltip-net-label]');
        if (!label || !income || !expense || !net) return;

        plot.dataset.cbpFlowReady = 'true';

        function show(index) {
            var group = groups[index];
            if (!group) return;
            groups.forEach(function (item, itemIndex) {
                item.classList.toggle('is-active', itemIndex === index);
            });
            plot.setAttribute('data-active', 'true');
            label.textContent = group.getAttribute('data-flow-label') || '';
            income.textContent = group.getAttribute('data-flow-income') || '';
            expense.textContent = group.getAttribute('data-flow-expense') || '';
            net.textContent = group.getAttribute('data-flow-net') || '';
            if (netLabel) netLabel.textContent = group.getAttribute('data-flow-net-label') || 'Resultado do período';
            var tone = group.getAttribute('data-flow-net-tone') || 'is-neutral';
            net.classList.remove('is-positive', 'is-negative', 'is-neutral');
            net.classList.add(tone);
            tooltip.hidden = false;
        }

        function hide() {
            groups.forEach(function (item) {
                item.classList.remove('is-active');
            });
            plot.removeAttribute('data-active');
            tooltip.hidden = true;
        }

        function focusGroup(index) {
            var target = groups[(index + groups.length) % groups.length];
            if (target) target.focus();
        }

        plot.__cbpFlowHide = hide;

        groups.forEach(function (group, index) {
            group.addEventListener('pointerenter', function () {
                show(index);
            });
            group.addEventListener('pointerleave', function () {
                if (document.activeElement !== group) hide();
            });
            group.addEventListener('focus', function () {
                show(index);
            });
            group.addEventListener('blur', function () {
                hide();
            });
            group.addEventListener('click', function () {
                show(index);
            });
            group.addEventListener('keydown', function (event) {
                if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
                    event.preventDefault();
                    focusGroup(index + 1);
                } else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
                    event.preventDefault();
                    focusGroup(index - 1);
                } else if (event.key === 'Home') {
                    event.preventDefault();
                    focusGroup(0);
                } else if (event.key === 'End') {
                    event.preventDefault();
                    focusGroup(groups.length - 1);
                } else if (event.key === 'Escape') {
                    event.preventDefault();
                    hide();
                }
            });
        });

        if (window.__cbPreviewFlowOutsideBound !== true) {
            window.__cbPreviewFlowOutsideBound = true;
            document.addEventListener('click', function (event) {
                var active = document.querySelector('[data-cbp-flow-plot][data-active]');
                if (active && !active.contains(event.target) && typeof active.__cbpFlowHide === 'function') {
                    active.__cbpFlowHide();
                }
            });
        }
    }

    function applyFlowCollapse() {
        var card = document.querySelector('[data-cbp-flow-root]');
        if (card && typeof card.__cbpFlowApply === 'function') card.__cbpFlowApply();
    }

    function initialize() {
        initFocusCards(document);
        initFinanceTabs(document);
        initFlowCollapse(document);
        initFlow(document);
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        initialize();
    }
    document.addEventListener('htmx:afterSwap', initialize);
    document.addEventListener('htmx:afterSettle', initialize);
    window.addEventListener('resize', applyFlowCollapse);
}());
