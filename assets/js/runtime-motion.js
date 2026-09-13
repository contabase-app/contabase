(function() {
    var animations = new WeakMap();

    function isSupported(element) {
        return Boolean(element && typeof element.animate === 'function');
    }

    function prefersReducedMotion() {
        return Boolean(window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches);
    }

    function cancel(element) {
        var animation = animations.get(element);
        if (!animation) return;
        animation.onfinish = null;
        animation.cancel();
        animations.delete(element);
    }

    function hold(element, frame) {
        if (!isSupported(element)) return null;

        var animation = animations.get(element);
        if (animation && animation.playState === 'paused' && animation.effect && typeof animation.effect.setKeyframes === 'function') {
            animation.effect.setKeyframes([frame, frame]);
            return animation;
        }

        cancel(element);
        animation = element.animate([frame, frame], { duration: 1, fill: 'both' });
        animation.pause();
        animations.set(element, animation);
        return animation;
    }

    function animate(element, target, options) {
        if (!isSupported(element)) return null;

        var config = options || {};
        var computed = window.getComputedStyle(element);
        var initial = {};
        Object.keys(target).forEach(function(property) {
            initial[property] = computed[property];
        });

        cancel(element);

        var complete = typeof config.complete === 'function' ? config.complete : null;
        var duration = prefersReducedMotion() ? 0 : Math.max(0, Number(config.duration) || 0);
        if (duration === 0) {
            if (complete) complete();
            return null;
        }

        var animation = element.animate([initial, target], {
            duration: duration,
            easing: config.easing || 'ease',
            fill: 'forwards'
        });
        animations.set(element, animation);
        animation.onfinish = function() {
            if (animations.get(element) !== animation) return;
            if (config.restore !== false) {
                animation.cancel();
                animations.delete(element);
            }
            if (complete) complete();
        };
        return animation;
    }

    window.cbRuntimeMotion = {
        cancel: cancel,
        hold: hold,
        toBase: function(element, target, options) {
            var config = Object.assign({}, options || {}, { restore: true });
            return animate(element, target, config);
        },
        exit: function(element, target, options) {
            var config = Object.assign({}, options || {}, { restore: false });
            return animate(element, target, config);
        },
        supported: typeof Element !== 'undefined' && typeof Element.prototype.animate === 'function'
    };
})();
