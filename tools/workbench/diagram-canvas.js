const MIN_SCALE = 0.1;
const MAX_SCALE = 4;
const SCALE_STEP = 1.2;
const instances = new WeakMap();

/**
 * Adds pan and zoom behavior to a scrollable SVG viewport.
 *
 * @param {HTMLElement} viewport
 * @param {SVGSVGElement} svg
 * @param {{width:number,height:number,onScaleChange?:(scale:number)=>void}} options
 */
export function attachDiagramCanvas(viewport, svg, { width, height, onScaleChange } = {}) {
  if (!viewport || !svg) throw new TypeError('A viewport and SVG element are required');

  const baseWidth = Number(width);
  const baseHeight = Number(height);
  if (!Number.isFinite(baseWidth) || !Number.isFinite(baseHeight) || baseWidth <= 0 || baseHeight <= 0) {
    throw new RangeError('Diagram width and height must be positive numbers');
  }

  instances.get(viewport)?.destroy();

  const originalStyles = {
    width: svg.style.width,
    height: svg.style.height,
    minWidth: svg.style.minWidth,
    marginInline: svg.style.marginInline,
    userSelect: viewport.style.userSelect,
    webkitUserSelect: viewport.style.webkitUserSelect,
    touchAction: viewport.style.touchAction,
    cursor: viewport.style.cursor,
    wasPanning: viewport.classList.contains('is-panning'),
  };

  let scale = 1;
  let destroyed = false;
  let drag = null;
  let pinch = null;
  const activePointers = new Map();

  const clampScale = value => Math.min(MAX_SCALE, Math.max(MIN_SCALE, value));
  const reportScale = () => onScaleChange?.(scale);

  function setScale(value, anchor) {
    const next = clampScale(value);
    const previous = scale;
    if (next === previous) return;

    let scrollLeft = viewport.scrollLeft;
    let scrollTop = viewport.scrollTop;
    let localX = 0;
    let localY = 0;
    let paddingLeft = 0;
    let paddingTop = 0;
    if (anchor) {
      const rect = viewport.getBoundingClientRect();
      const style = getComputedStyle(viewport);
      const borderLeft = parseFloat(style.borderLeftWidth) || 0;
      const borderTop = parseFloat(style.borderTopWidth) || 0;
      paddingLeft = parseFloat(style.paddingLeft) || 0;
      paddingTop = parseFloat(style.paddingTop) || 0;
      localX = anchor.clientX - rect.left - borderLeft;
      localY = anchor.clientY - rect.top - borderTop;
      scrollLeft = paddingLeft + ((viewport.scrollLeft + localX - paddingLeft) / previous) * next - localX;
      scrollTop = paddingTop + ((viewport.scrollTop + localY - paddingTop) / previous) * next - localY;
    }

    scale = next;
    svg.style.width = `${baseWidth * scale}px`;
    svg.style.height = 'auto';
    svg.style.marginInline = '0';

    if (anchor) {
      viewport.scrollLeft = scrollLeft;
      viewport.scrollTop = scrollTop;
    }
    reportScale();
  }

  function centerAnchor() {
    const rect = viewport.getBoundingClientRect();
    const style = getComputedStyle(viewport);
    const borderLeft = parseFloat(style.borderLeftWidth) || 0;
    const borderTop = parseFloat(style.borderTopWidth) || 0;
    return {
      clientX: rect.left + borderLeft + viewport.clientWidth / 2,
      clientY: rect.top + borderTop + viewport.clientHeight / 2,
    };
  }

  function zoomIn() {
    setScale(scale * SCALE_STEP, centerAnchor());
  }

  function zoomOut() {
    setScale(scale / SCALE_STEP, centerAnchor());
  }

  function fit() {
    const style = getComputedStyle(viewport);
    const paddingX = (parseFloat(style.paddingLeft) || 0) + (parseFloat(style.paddingRight) || 0);
    const paddingY = (parseFloat(style.paddingTop) || 0) + (parseFloat(style.paddingBottom) || 0);
    const availableWidth = Math.max(0, viewport.clientWidth - paddingX);
    const availableHeight = Math.max(0, viewport.clientHeight - paddingY);
    if (!availableWidth || !availableHeight) return;
    setScale(Math.min(availableWidth / baseWidth, availableHeight / baseHeight));
    viewport.scrollLeft = 0;
    viewport.scrollTop = 0;
  }

  function reset() {
    setScale(1);
    viewport.scrollLeft = 0;
    viewport.scrollTop = 0;
  }

  function isEditableTarget(target) {
    if (!(target instanceof Element)) return false;
    return Boolean(target.closest('input, textarea, select, button, [role="textbox"], [contenteditable]:not([contenteditable="false"])'));
  }

  function onKeyDown(event) {
    if (event.defaultPrevented || event.isComposing || event.ctrlKey || event.metaKey || event.altKey || isEditableTarget(event.target)) return;
    if (event.key === '+' || event.key === '=') {
      event.preventDefault();
      zoomIn();
    } else if (event.key === '-') {
      event.preventDefault();
      zoomOut();
    } else if (event.key === '0') {
      event.preventDefault();
      reset();
    }
  }

  function isTraditionalWheel(event) {
    if (event.deltaMode !== WheelEvent.DOM_DELTA_PIXEL) return true;
    const legacyDelta = Number(event.wheelDeltaY ?? event.wheelDelta);
    return Number.isFinite(legacyDelta) && legacyDelta !== 0 && Math.abs(legacyDelta) % 120 === 0;
  }

  function onWheel(event) {
    if (!(event.ctrlKey || event.metaKey || isTraditionalWheel(event))) return;
    event.preventDefault();
    const vertical = event.deltaY !== 0;
    let delta = vertical ? event.deltaY : event.deltaX;
    if (!delta) return;
    if (event.deltaMode === WheelEvent.DOM_DELTA_LINE) {
      const style = getComputedStyle(viewport);
      const lineHeight = parseFloat(style.lineHeight) || parseFloat(style.fontSize) || 16;
      delta *= lineHeight;
    } else if (event.deltaMode === WheelEvent.DOM_DELTA_PAGE) {
      delta *= vertical ? viewport.clientHeight : viewport.clientWidth;
    }
    setScale(scale * Math.exp(-delta * 0.002), { clientX: event.clientX, clientY: event.clientY });
  }

  function isOverScrollbar(event) {
    const rect = viewport.getBoundingClientRect();
    const right = rect.left + viewport.clientLeft + viewport.clientWidth;
    const bottom = rect.top + viewport.clientTop + viewport.clientHeight;
    return event.clientX >= right || event.clientY >= bottom;
  }

  function updatePointer(event) {
    activePointers.set(event.pointerId, {
      x: event.clientX,
      y: event.clientY,
      type: event.pointerType,
    });
  }

  function touchPair() {
    const touches = [...activePointers.entries()].filter(([, pointer]) => pointer.type === 'touch');
    return touches.length >= 2 ? touches.slice(0, 2) : null;
  }

  function pairGeometry(pair) {
    const [first, second] = pair.map(([, point]) => point);
    const dx = second.x - first.x;
    const dy = second.y - first.y;
    return {
      distance: Math.max(1, Math.hypot(dx, dy)),
      centerX: (first.x + second.x) / 2,
      centerY: (first.y + second.y) / 2,
    };
  }

  function setPanning(active) {
    viewport.classList.toggle('is-panning', active || originalStyles.wasPanning);
    viewport.style.cursor = active ? 'grabbing' : 'grab';
  }

  function onPointerDown(event) {
    if ((event.pointerType === 'mouse' || event.pointerType === 'pen') && event.button !== 0) return;
    if (isOverScrollbar(event)) return;

    viewport.focus({ preventScroll: true });
    updatePointer(event);
    try { viewport.setPointerCapture(event.pointerId); } catch {}

    const pair = touchPair();
    if (pair) {
      drag = null;
      const geometry = pairGeometry(pair);
      pinch = { ...geometry, scale };
      setPanning(true);
      event.preventDefault();
      return;
    }

    if (activePointers.size === 1) {
      drag = {
        pointerId: event.pointerId,
        startX: event.clientX,
        startY: event.clientY,
        scrollLeft: viewport.scrollLeft,
        scrollTop: viewport.scrollTop,
        moved: false,
      };
      event.preventDefault();
    }
  }

  function onPointerMove(event) {
    if (!activePointers.has(event.pointerId)) return;
    updatePointer(event);

    const pair = touchPair();
    if (pair && pinch) {
      const geometry = pairGeometry(pair);
      const oldCenter = { clientX: pinch.centerX, clientY: pinch.centerY };
      const nextScale = pinch.scale * geometry.distance / pinch.distance;
      setScale(nextScale, oldCenter);
      viewport.scrollLeft -= geometry.centerX - pinch.centerX;
      viewport.scrollTop -= geometry.centerY - pinch.centerY;
      pinch = { ...geometry, scale };
      event.preventDefault();
      return;
    }

    if (!drag || drag.pointerId !== event.pointerId) return;
    const dx = event.clientX - drag.startX;
    const dy = event.clientY - drag.startY;
    if (!drag.moved && Math.hypot(dx, dy) >= 3) {
      drag.moved = true;
      setPanning(true);
    }
    if (drag.moved) {
      viewport.scrollLeft = drag.scrollLeft - dx;
      viewport.scrollTop = drag.scrollTop - dy;
      event.preventDefault();
    }
  }

  function endPointer(event) {
    activePointers.delete(event.pointerId);

    if (pinch) {
      const remainingPair = touchPair();
      if (remainingPair) {
        drag = null;
        pinch = { ...pairGeometry(remainingPair), scale };
      } else {
        pinch = null;
        const remaining = activePointers.entries().next();
        if (!remaining.done) {
          const [pointerId, pointer] = remaining.value;
          drag = {
            pointerId,
            startX: pointer.x,
            startY: pointer.y,
            scrollLeft: viewport.scrollLeft,
            scrollTop: viewport.scrollTop,
            moved: false,
          };
        } else {
          drag = null;
        }
      }
    } else if (drag?.pointerId === event.pointerId) {
      drag = null;
    }

    if (!pinch && !drag?.moved) setPanning(false);
    try {
      if (viewport.hasPointerCapture(event.pointerId)) viewport.releasePointerCapture(event.pointerId);
    } catch {}
  }

  function onLostPointerCapture(event) {
    if (activePointers.has(event.pointerId)) endPointer(event);
  }

  // Prevent text selection during a pan, while leaving all neighboring content alone.
  viewport.style.userSelect = 'none';
  viewport.style.webkitUserSelect = 'none';
  viewport.style.touchAction = 'none';
  viewport.style.cursor = 'grab';
  svg.style.width = `${baseWidth}px`;
  svg.style.height = 'auto';
  svg.style.minWidth = '0';
  svg.style.marginInline = '0';

  viewport.addEventListener('keydown', onKeyDown);
  viewport.addEventListener('wheel', onWheel, { passive: false });
  viewport.addEventListener('pointerdown', onPointerDown);
  viewport.addEventListener('pointermove', onPointerMove);
  viewport.addEventListener('pointerup', endPointer);
  viewport.addEventListener('pointercancel', endPointer);
  viewport.addEventListener('lostpointercapture', onLostPointerCapture);

  const controller = {
    zoomIn,
    zoomOut,
    fit,
    reset,
    destroy() {
      if (destroyed) return;
      destroyed = true;
      for (const pointerId of activePointers.keys()) {
        try {
          if (viewport.hasPointerCapture(pointerId)) viewport.releasePointerCapture(pointerId);
        } catch {}
      }
      viewport.removeEventListener('keydown', onKeyDown);
      viewport.removeEventListener('wheel', onWheel);
      viewport.removeEventListener('pointerdown', onPointerDown);
      viewport.removeEventListener('pointermove', onPointerMove);
      viewport.removeEventListener('pointerup', endPointer);
      viewport.removeEventListener('pointercancel', endPointer);
      viewport.removeEventListener('lostpointercapture', onLostPointerCapture);
      viewport.classList.toggle('is-panning', originalStyles.wasPanning);
      activePointers.clear();
      drag = null;
      pinch = null;
      svg.style.width = originalStyles.width;
      svg.style.height = originalStyles.height;
      svg.style.minWidth = originalStyles.minWidth;
      svg.style.marginInline = originalStyles.marginInline;
      viewport.style.userSelect = originalStyles.userSelect;
      viewport.style.webkitUserSelect = originalStyles.webkitUserSelect;
      viewport.style.touchAction = originalStyles.touchAction;
      viewport.style.cursor = originalStyles.cursor;
      if (instances.get(viewport) === controller) instances.delete(viewport);
    },
  };

  instances.set(viewport, controller);
  reportScale();
  return controller;
}
