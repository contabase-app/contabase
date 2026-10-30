(function () {
    'use strict';

    function initSidebar(root) {
        var sidebar = root.querySelector('[data-v2-sidebar]');
        var toggle = root.querySelector('[data-v2-sidebar-toggle]');
        var storageKey = 'contabase-app-shell-sidebar-collapsed';
        var legacyStorageKeys = ['contabase-app-shell-v2-sidebar-collapsed', 'contabase-dashboard-v2-sidebar-collapsed'];
        if (!sidebar || !toggle || sidebar.dataset.v2SidebarReady === 'true') return;
        sidebar.dataset.v2SidebarReady = 'true';

        var update = function (collapsed, persist) {
            root.classList.toggle('is-sidebar-collapsed', collapsed);
            toggle.setAttribute('aria-expanded', String(!collapsed));
            toggle.setAttribute('aria-label', collapsed ? 'Expandir menu lateral' : 'Recolher menu lateral');
            toggle.setAttribute('title', collapsed ? 'Expandir menu lateral' : 'Recolher menu lateral');
            if (!persist) return;
            try {
                window.localStorage.setItem(storageKey, String(collapsed));
            } catch (error) {
                // The visual control still works when browser storage is unavailable.
            }
        };

        try {
            var saved = window.localStorage.getItem(storageKey);
            if (saved === null) {
                legacyStorageKeys.some(function (key) {
                    saved = window.localStorage.getItem(key);
                    return saved !== null;
                });
            }
            update(saved === 'true', false);
        } catch (error) {
            update(false, false);
        }

        toggle.addEventListener('click', function () {
            update(!root.classList.contains('is-sidebar-collapsed'), true);
        });
    }

    function initDrawer(root) {
        var dialog = root.querySelector('[data-v2-menu-dialog]');
        var trigger = root.querySelector('[data-v2-menu-trigger]');
        if (!dialog || !trigger || dialog.dataset.v2DrawerReady === 'true') return;
        dialog.dataset.v2DrawerReady = 'true';

        trigger.addEventListener('click', function () {
            if (!dialog.open) dialog.showModal();
        });
        dialog.querySelectorAll('[data-v2-menu-close], a, button[hx-get]').forEach(function (item) {
            item.addEventListener('click', function () {
                if (dialog.open) dialog.close();
            });
        });
        dialog.addEventListener('click', function (event) {
            if (event.target === dialog && dialog.open) dialog.close();
        });
    }

    function initNavigationGroups(root) {
        root.querySelectorAll('[data-v2-nav-group]').forEach(function (group) {
            if (group.dataset.v2NavGroupReady === 'true') return;
            var toggle = group.querySelector('[data-v2-nav-group-toggle]');
            var panel = group.querySelector('[data-v2-nav-group-panel]');
            var key = group.getAttribute('data-v2-nav-group-key');
            if (!toggle || !panel || !key) return;
            group.dataset.v2NavGroupReady = 'true';

            var storageKey = 'contabase-app-shell-nav-group-' + key;
            var legacyStorageKey = 'contabase-app-shell-v2-nav-group-' + key;
            var defaultOpen = group.getAttribute('data-v2-nav-group-default') === 'open';
            var setOpen = function (open, persist) {
                group.classList.toggle('is-open', open);
                panel.hidden = !open;
                toggle.setAttribute('aria-expanded', String(open));
                if (!persist) return;
                try { window.localStorage.setItem(storageKey, String(open)); } catch (error) { /* keep the in-memory state */ }
            };

            try {
                var saved = window.localStorage.getItem(storageKey);
                if (saved === null) saved = window.localStorage.getItem(legacyStorageKey);
                setOpen(saved === null ? defaultOpen : saved === 'true', false);
            } catch (error) {
                setOpen(defaultOpen, false);
            }
            toggle.addEventListener('click', function () {
                setOpen(!group.classList.contains('is-open'), true);
            });
        });
    }

    function initWorkspaceDrawer(root) {
        var triggers = root.querySelectorAll('[data-v2-workspace-trigger]');
        var drawer = document.getElementById('workspaceDrawer');
        var backdrop = root.querySelector('[data-v2-workspace-backdrop]');
        if (!triggers.length || !drawer || drawer.dataset.v2WorkspaceDrawerReady === 'true') return;
        drawer.dataset.v2WorkspaceDrawerReady = 'true';

        triggers.forEach(function (trigger) {
            trigger.addEventListener('click', function () {
                var navigationDialog = trigger.closest('[data-v2-menu-dialog]');
                if (navigationDialog && navigationDialog.open) navigationDialog.close();
                if (typeof window.openWorkspaceDrawer === 'function') window.openWorkspaceDrawer();
            });
        });

        drawer.querySelectorAll('[data-v2-workspace-close]').forEach(function (close) {
            close.addEventListener('click', function () {
                if (typeof window.closeWorkspaceDrawer === 'function') window.closeWorkspaceDrawer();
            });
        });

        if (backdrop) {
            backdrop.addEventListener('click', function () {
                if (typeof window.closeWorkspaceDrawer === 'function') window.closeWorkspaceDrawer();
            });
        }
    }

    function syncNavigation(root) {
        var page = root.querySelector('[data-page-section]');
        if (!page) return;
        var section = page.getAttribute('data-page-section');
        if (!section) return;

        root.querySelectorAll('[data-v2-nav-section]').forEach(function (link) {
            var active = link.getAttribute('data-v2-nav-section') === section;
            link.classList.toggle('is-active', active);
            if (active) {
                link.setAttribute('aria-current', 'page');
            } else {
                link.removeAttribute('aria-current');
            }
        });
    }

    function syncNotificationBadges(root) {
        var source = document.getElementById('notification-badge');
        if (!source) return;
        root.querySelectorAll('[data-notification-badge]').forEach(function (badge) {
            badge.className = source.className;
            if (source.hasAttribute('aria-label')) {
                badge.setAttribute('aria-label', source.getAttribute('aria-label'));
            } else {
                badge.removeAttribute('aria-label');
            }
        });
    }

    function initThemeToggle(root) {
        var toggles = root.querySelectorAll('[data-v2-theme-toggle]');
        if (!toggles.length) return;

        var update = function () {
            var isDark = document.documentElement.classList.contains('dark');
            var nextTheme = isDark ? 'claro' : 'escuro';
            toggles.forEach(function (toggle) {
                toggle.setAttribute('aria-label', 'Alternar para tema ' + nextTheme);
                toggle.setAttribute('title', 'Alternar para tema ' + nextTheme);
            });
            var themeColor = document.querySelector('meta[name="theme-color"]');
            if (themeColor) themeColor.setAttribute('content', isDark ? '#161a20' : '#f3f4f6');
        };

        toggles.forEach(function (toggle) {
            if (toggle.dataset.v2ThemeReady === 'true') return;
            toggle.dataset.v2ThemeReady = 'true';
            toggle.addEventListener('click', function () {
                window.requestAnimationFrame(update);
            });
        });
        update();
    }

    function refreshIcons() {
        if (window.lucide && typeof window.lucide.createIcons === 'function') {
            window.lucide.createIcons();
        }
    }

    function initialize() {
        var root = document.querySelector('[data-app-shell]');
        if (!root) return;
        initSidebar(root);
        initDrawer(root);
        initNavigationGroups(root);
        initWorkspaceDrawer(root);
        syncNavigation(root);
        syncNotificationBadges(root);
        initThemeToggle(root);
        refreshIcons();
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initialize, { once: true });
    } else {
        initialize();
    }

    document.addEventListener('htmx:afterSwap', function () {
        var root = document.querySelector('[data-app-shell]');
        if (!root) return;
        syncNavigation(root);
        syncNotificationBadges(root);
        refreshIcons();
    });
}());
