function initDashboardTabs(root) {
    (root || document).querySelectorAll('[data-dashboard-tabs]').forEach(function(group) {
        if (group.dataset.tabsInit === 'true') return;
        group.dataset.tabsInit = 'true';
        var tabs = group.querySelectorAll('[data-dashboard-tab]');
        var panels = group.querySelectorAll('[data-dashboard-panel]');
        tabs.forEach(function(tab) {
            tab.addEventListener('click', function() {
                var target = tab.dataset.dashboardTab;
                tabs.forEach(function(item) {
                    item.classList.toggle('is-active', item === tab);
                });
                panels.forEach(function(panel) {
                    panel.classList.toggle('hidden', panel.dataset.dashboardPanel !== target);
                });
            });
        });
    });
}
