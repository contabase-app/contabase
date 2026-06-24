(function () {
    var ACTIVE_CLASSES = {
        dashboard: {
            active: 'bg-white text-indigo-650 shadow-sm dark:bg-zinc-900 dark:text-indigo-400 font-black',
            inactive: 'text-zinc-500 hover:text-zinc-700 dark:text-zinc-400 dark:hover:text-zinc-200 font-bold'
        },
        contas: {
            active: 'bg-white text-indigo-650 shadow-sm dark:bg-zinc-900 dark:text-indigo-400 font-black',
            inactive: 'text-zinc-550 hover:text-zinc-750 dark:text-zinc-400 dark:hover:text-zinc-200 font-bold'
        }
    };

    function getTabConfig(groupName) {
        return ACTIVE_CLASSES[groupName] || {
            active: 'bg-white text-indigo-650 shadow-sm dark:bg-zinc-900 dark:text-indigo-400 font-black',
            inactive: 'text-zinc-500 hover:text-zinc-700 dark:text-zinc-400 dark:hover:text-zinc-200 font-bold'
        };
    }

    function activateTab(groupName, tabName) {
        var group = document.querySelector('[data-tab-group="' + groupName + '"]');
        if (!group) return;

        var config = getTabConfig(groupName);
        var activeClasses = config.active.split(' ');
        var inactiveClasses = config.inactive.split(' ');

        group.querySelectorAll('[data-tab]').forEach(function (btn) {
            var isActive = btn.getAttribute('data-tab') === tabName;
            if (isActive) {
                btn.classList.remove.apply(btn.classList, inactiveClasses);
                btn.classList.add.apply(btn.classList, activeClasses);
            } else {
                btn.classList.remove.apply(btn.classList, activeClasses);
                btn.classList.add.apply(btn.classList, inactiveClasses);
            }
        });

        group.querySelectorAll('[data-tab-panel]').forEach(function (panel) {
            var isActive = panel.getAttribute('data-tab-panel') === tabName;
            if (isActive) {
                panel.classList.remove('hidden');
            } else {
                panel.classList.add('hidden');
            }
        });

        if (group.hasAttribute('data-tab-persist')) {
            var key = group.getAttribute('data-tab-persist');
            try { localStorage.setItem(key, tabName); } catch (e) { /* noop */ }
        }
    }

    window.switchTab = function (e) {
        var btn = e.target.closest('[data-tab]');
        if (!btn) return;
        var tab = btn.getAttribute('data-tab');
        var group = btn.getAttribute('data-tab-group');
        if (group && tab) activateTab(group, tab);
    };

    function initTabGroups() {
        document.querySelectorAll('[data-tab-group]').forEach(function (group) {
            var groupName = group.getAttribute('data-tab-group');
            if (group.hasAttribute('data-tab-initialized')) return;

            var defaultTab = group.getAttribute('data-tab-default');
            var persistKey = group.getAttribute('data-tab-persist');

            var activeTab = defaultTab;
            if (persistKey) {
                try {
                    var saved = localStorage.getItem(persistKey);
                    if (saved) activeTab = saved;
                } catch (e) { /* noop */ }
            }

            if (activeTab) {
                activateTab(groupName, activeTab);
            }

            group.setAttribute('data-tab-initialized', '');
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initTabGroups);
    } else {
        initTabGroups();
    }

    document.body.addEventListener('htmx:afterSettle', function () {
        initTabGroups();
    });
})();
