(function () {
    window.toggleShowMore = function (e) {
        var btn = e.target.closest('[data-show-more-toggle]');
        if (!btn) return;

        var group = btn.closest('[data-show-more-group]');
        if (!group) return;

        var initial = parseInt(group.getAttribute('data-show-more-initial')) || 0;
        var isOneWay = group.hasAttribute('data-show-more-one-way');

        if (isOneWay) {
            showAllItems(group, initial);
        } else {
            toggleItems(group, initial);
        }
    };

    function showAllItems(group, initial) {
        group.querySelectorAll('[data-show-more-item]').forEach(function (item) {
            var idx = parseInt(item.getAttribute('data-show-more-item'));
            if (!isNaN(idx) && idx >= initial) {
                item.classList.remove('hidden');
            }
        });

        var toggleContainer = group.querySelector('[data-show-more-toggle-container]');
        if (toggleContainer) {
            toggleContainer.classList.add('hidden');
        }
    }

    function toggleItems(group, initial) {
        var isExpanded = group.classList.contains('show-more-expanded');
        var items = group.querySelectorAll('[data-show-more-item]');

        items.forEach(function (item) {
            var idx = parseInt(item.getAttribute('data-show-more-item'));
            if (!isNaN(idx) && idx >= initial) {
                if (!isExpanded) {
                    item.classList.remove('hidden');
                } else {
                    item.classList.add('hidden');
                }
            }
        });

        group.classList.toggle('show-more-expanded');
        isExpanded = group.classList.contains('show-more-expanded');

        var expandLabel = group.querySelector('[data-show-more-label-expand]');
        var collapseLabel = group.querySelector('[data-show-more-label-collapse]');

        if (expandLabel) {
            expandLabel.classList.toggle('hidden', isExpanded);
        }
        if (collapseLabel) {
            collapseLabel.classList.toggle('hidden', !isExpanded);
        }
    }

    function initShowMore() {
        document.querySelectorAll('[data-show-more-group]').forEach(function (group) {
            if (group.hasAttribute('data-show-more-initialized')) return;

            var initial = parseInt(group.getAttribute('data-show-more-initial')) || 5;
            var hasHidden = false;

            group.querySelectorAll('[data-show-more-item]').forEach(function (item) {
                var idx = parseInt(item.getAttribute('data-show-more-item'));
                if (!isNaN(idx) && idx >= initial) {
                    item.classList.add('hidden');
                    hasHidden = true;
                }
            });

            if (!hasHidden) {
                var toggleContainer = group.querySelector('[data-show-more-toggle-container]');
                if (toggleContainer) toggleContainer.classList.add('hidden');
            }

            group.setAttribute('data-show-more-initialized', '');
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', initShowMore);
    } else {
        initShowMore();
    }

    document.body.addEventListener('htmx:afterSettle', function () {
        initShowMore();
    });
})();
