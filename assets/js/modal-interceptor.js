// Modal ESC Key Interceptor and Picker Backdrop Click
// Handles Escape key and closing pickers
(function() {
    function consumeHandledEscape(e) {
        e.preventDefault();
        e.stopImmediatePropagation();
    }

    window.closePickerModal = function() {
        var picker = document.getElementById('modalPickerBackdrop');
        if (picker) {
            picker.remove();
            if (typeof window.overlayDidClose === 'function') window.overlayDidClose();
        }
    };

    document.addEventListener('click', function(e) {
        var backdrop = e.target.closest('[data-picker-backdrop]');
        if (!backdrop) return;
        if (e.target !== backdrop) return;
        e.stopPropagation();
        if (window.closePickerModal) window.closePickerModal();
        else backdrop.remove();
    });

    window.addEventListener('keydown', function(e) {
        if (e.key !== 'Escape') return;
        if (typeof window.closeTopOverlay === 'function' && window.closeTopOverlay('escape')) {
            consumeHandledEscape(e);
        }
    }, true);
})();
