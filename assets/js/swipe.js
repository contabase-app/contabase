function resetSwipe(row) {
    var content = row.querySelector('[data-swipe-content]');
    var leftBg = row.querySelector('[data-swipe-bg-left]');
    var rightBg = row.querySelector('[data-swipe-bg-right]');
    if (content) {
        content.style.transition = 'transform 220ms cubic-bezier(0.22, 1, 0.36, 1)';
        content.style.transform = '';
    }
    [leftBg, rightBg].forEach(function(bg) {
        if (!bg) return;
        bg.style.opacity = '0';
        bg.style.transform = 'scaleX(0.88)';
    });
    window.setTimeout(function() {
        if (content) content.style.transition = '';
    }, 240);
}

function resetAllSwipes() {
    document.querySelectorAll('[data-swipe-row="true"]').forEach(resetSwipe);
}

function runSwipeAction(row, side) {
    var txId = row.dataset.txId;
    var url;
    if (side === 'left') {
        url = row.dataset.swipeLeftUrl;
        if (!url && txId) url = '/transacoes/' + txId + '?escopo=single';
    } else {
        url = row.dataset.swipeRightUrl;
        if (!url && txId) url = '/transacoes/' + txId + '/status-pagamento';
    }
    if (!url || !window.htmx) return;
    if (side === 'left' && row.dataset.isSeries === 'true') {
        window.openSwipeDeleteModal(txId, row);
        return;
    }
    var confirmText = side === 'left' ? row.dataset.swipeLeftConfirm : row.dataset.swipeRightConfirm;
    if (!confirmText && txId && side === 'left') confirmText = 'Remover este lançamento?';

    var doAction = function() {
        window._swipeActionTs = Date.now();
        var method, swap;
        if (side === 'left') {
            method = row.dataset.swipeLeftMethod || 'DELETE';
            swap = row.dataset.swipeLeftSwap || (txId ? 'delete' : 'none');
        } else {
            method = row.dataset.swipeRightMethod || 'POST';
            swap = row.dataset.swipeRightSwap || (txId ? 'outerHTML' : 'none');
        }
        var target = side === 'left' ? row.dataset.swipeLeftTarget : row.dataset.swipeRightTarget;
        if (!target && txId) target = '#tx-' + txId;
        var options = { swap: swap };
        if (target) options.target = target;
        htmx.ajax(method, url, options);
    };

    if (confirmText) {
        var isDestructive = side === 'left';
        window.openGlobalConfirm(confirmText, doAction, isDestructive);
    } else {
        doAction();
    }
}

function initSwipeRow(row) {
    if (row.dataset.swipeInit === 'true') return;
    var content = row.querySelector('[data-swipe-content]');
    if (!content) return;
    row.dataset.swipeInit = 'true';

    var startX = 0;
    var startY = 0;
    var currentX = 0;
    var horizontal = false;
    var tracking = false;
    var leftBg = row.querySelector('[data-swipe-bg-left]');
    var rightBg = row.querySelector('[data-swipe-bg-right]');

    row.addEventListener('touchstart', function(event) {
        if (!event.touches || event.touches.length !== 1) return;
        startX = event.touches[0].clientX;
        startY = event.touches[0].clientY;
        currentX = startX;
        horizontal = false;
        tracking = true;
        content.style.transition = '';
    }, { passive: true });

    row.addEventListener('touchmove', function(event) {
        if (!tracking || !event.touches || event.touches.length !== 1) return;
        currentX = event.touches[0].clientX;
        var currentY = event.touches[0].clientY;
        var diffX = currentX - startX;
        var diffY = currentY - startY;
        if (!horizontal && Math.abs(diffX) < 10) return;
        if (!horizontal && Math.abs(diffY) > Math.abs(diffX)) {
            tracking = false;
            resetSwipe(row);
            return;
        }
        horizontal = true;
        row.dispatchEvent(new CustomEvent('swipe-started', { bubbles: true }));
        if (event.cancelable) event.preventDefault();

        var limited = Math.max(Math.min(diffX * 0.48, 140), -140);
        content.style.transform = 'translateX(' + limited + 'px)';
        if (diffX > 0 && leftBg) {
            leftBg.style.opacity = Math.min(Math.abs(diffX) / 84, 1);
            leftBg.style.transform = 'scaleX(' + Math.min(1, 0.88 + Math.abs(diffX) / 700) + ')';
        } else if (leftBg) {
            leftBg.style.opacity = '0';
            leftBg.style.transform = 'scaleX(0.88)';
        }
        if (diffX < 0 && rightBg) {
            rightBg.style.opacity = Math.min(Math.abs(diffX) / 84, 1);
            rightBg.style.transform = 'scaleX(' + Math.min(1, 0.88 + Math.abs(diffX) / 700) + ')';
        } else if (rightBg) {
            rightBg.style.opacity = '0';
            rightBg.style.transform = 'scaleX(0.88)';
        }
    }, { passive: false });

    row.addEventListener('touchend', function() {
        if (!tracking && !horizontal) return;
        var diff = currentX - startX;
        tracking = false;
        horizontal = false;
        resetSwipe(row);
        if (diff < -100) {
            runSwipeAction(row, 'right');
        } else if (diff > 100) {
            runSwipeAction(row, 'left');
        }
    });

    row.addEventListener('touchcancel', function() {
        tracking = false;
        horizontal = false;
        resetSwipe(row);
    });
}

function initSwipeRows(root) {
    var scope = root || document;
    if (scope.querySelectorAll) {
        scope.querySelectorAll('[data-swipe-row="true"]').forEach(initSwipeRow);
    }
    if (scope !== document) {
        document.querySelectorAll('[data-swipe-row="true"]:not([data-swipe-init="true"])').forEach(initSwipeRow);
    }
}

initSwipeRows(document);
