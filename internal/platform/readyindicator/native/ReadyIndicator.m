#import "ReadyIndicator.h"

#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>
#import <CoreGraphics/CoreGraphics.h>
#import <math.h>

static const CGFloat kMiMacReadyDotDiameter = 8.0;
static const CFTimeInterval kMiMacReadyPulseDuration = 1.5;

@interface MiMacReadyIndicatorOverlayView : NSView
@property(nonatomic, strong) CAShapeLayer *haloLayer;
@property(nonatomic, strong) CAShapeLayer *dotLayer;
@property(nonatomic, strong) id occlusionObserver;
@property(nonatomic, strong) id displayOptionsObserver;
@property(nonatomic) CGRect placeholderRect;
@property(nonatomic) BOOL hasPlaceholder;
@property(nonatomic) BOOL animateRequested;
@property(nonatomic) BOOL closed;
@property(nonatomic) BOOL pulseRunning;
@property(nonatomic) uint32_t rgba;
- (BOOL)updateRect:(CGRect)rect rgba:(uint32_t)rgba animate:(BOOL)animate;
- (void)closeIndicator;
@end

@implementation MiMacReadyIndicatorOverlayView

- (instancetype)initWithFrame:(NSRect)frame {
    self = [super initWithFrame:frame];
    if (!self) return nil;

    self.wantsLayer = YES;
    self.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    self.layer.backgroundColor = NSColor.clearColor.CGColor;
    self.layer.masksToBounds = NO;

    self.haloLayer = [CAShapeLayer layer];
    self.haloLayer.bounds = CGRectMake(0, 0, kMiMacReadyDotDiameter, kMiMacReadyDotDiameter);
    self.haloLayer.anchorPoint = CGPointMake(0.5, 0.5);
    CGPathRef haloPath = CGPathCreateWithEllipseInRect(self.haloLayer.bounds, NULL);
    self.haloLayer.path = haloPath;
    CGPathRelease(haloPath);
    self.haloLayer.opacity = 0;
    [self.layer addSublayer:self.haloLayer];

    self.dotLayer = [CAShapeLayer layer];
    self.dotLayer.bounds = CGRectMake(0, 0, kMiMacReadyDotDiameter, kMiMacReadyDotDiameter);
    self.dotLayer.anchorPoint = CGPointMake(0.5, 0.5);
    CGPathRef dotPath = CGPathCreateWithEllipseInRect(self.dotLayer.bounds, NULL);
    self.dotLayer.path = dotPath;
    CGPathRelease(dotPath);
    [self.layer addSublayer:self.dotLayer];

    self.rgba = 0x30D158FF;
    [self applyColor];
    return self;
}

- (BOOL)acceptsFirstResponder { return NO; }
- (BOOL)isOpaque { return NO; }
- (NSView *)hitTest:(NSPoint)point {
    (void)point;
    return nil;
}

- (void)viewDidMoveToWindow {
    [super viewDidMoveToWindow];
    [self removeObservers];
    NSWindow *window = self.window;
    if (window) {
        __weak MiMacReadyIndicatorOverlayView *weakSelf = self;
        NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
        self.occlusionObserver = [center addObserverForName:NSWindowDidChangeOcclusionStateNotification
                                                      object:window
                                                       queue:NSOperationQueue.mainQueue
                                                  usingBlock:^(__unused NSNotification *note) {
            [weakSelf updatePulseState];
        }];
        NSWorkspace *workspace = NSWorkspace.sharedWorkspace;
        self.displayOptionsObserver = [workspace.notificationCenter
            addObserverForName:NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification
                         object:workspace
                          queue:NSOperationQueue.mainQueue
                     usingBlock:^(__unused NSNotification *note) {
            [weakSelf updatePulseState];
        }];
    }
    [self updatePulseState];
}

- (void)layout {
    [super layout];
    [self positionLayers];
}

- (BOOL)updateRect:(CGRect)rect rgba:(uint32_t)rgba animate:(BOOL)animate {
    if (!NSThread.isMainThread || self.closed ||
        !isfinite(rect.origin.x) || !isfinite(rect.origin.y) ||
        !isfinite(rect.size.width) || !isfinite(rect.size.height) ||
        rect.size.width <= 0 || rect.size.height <= 0) {
        return NO;
    }

    self.placeholderRect = rect;
    self.hasPlaceholder = YES;
    self.animateRequested = animate;
    self.rgba = rgba;
    [self applyColor];
    [self positionLayers];
    self.hidden = NO;
    [self updatePulseState];
    return YES;
}

- (void)positionLayers {
    if (!self.hasPlaceholder) return;

    CGFloat x = CGRectGetMidX(self.placeholderRect);
    CGFloat y = NSMaxY(self.bounds) - CGRectGetMidY(self.placeholderRect);
    CGPoint center = CGPointMake(x, y);
    [CATransaction begin];
    [CATransaction setDisableActions:YES];
    self.haloLayer.position = center;
    self.dotLayer.position = center;
    [CATransaction commit];
}

- (void)applyColor {
    CGFloat red = ((self.rgba >> 24) & 0xff) / 255.0;
    CGFloat green = ((self.rgba >> 16) & 0xff) / 255.0;
    CGFloat blue = ((self.rgba >> 8) & 0xff) / 255.0;
    CGFloat alpha = (self.rgba & 0xff) / 255.0;
    CGColorSpaceRef colorSpace = CGColorSpaceCreateWithName(kCGColorSpaceSRGB);
    if (!colorSpace) return;
    CGFloat components[] = {red, green, blue, alpha};
    CGColorRef color = CGColorCreate(colorSpace, components);
    CGColorSpaceRelease(colorSpace);
    if (!color) return;

    [CATransaction begin];
    [CATransaction setDisableActions:YES];
    self.haloLayer.fillColor = color;
    self.dotLayer.fillColor = color;
    [CATransaction commit];
    CGColorRelease(color);
}

- (BOOL)windowCanAnimate {
    NSWindow *window = self.window;
    if (!window || !window.isVisible || window.isMiniaturized || self.isHiddenOrHasHiddenAncestor) return NO;
    if ((window.occlusionState & NSWindowOcclusionStateVisible) == 0) return NO;
    return !NSWorkspace.sharedWorkspace.accessibilityDisplayShouldReduceMotion;
}

- (void)updatePulseState {
    if (self.closed || !self.hasPlaceholder) {
        [self stopPulseWithOpacity:0];
        return;
    }
    if (self.animateRequested && [self windowCanAnimate]) {
        [self startPulseIfNeeded];
    } else {
        [self stopPulseWithOpacity:0.10];
    }
}

- (void)startPulseIfNeeded {
    if (self.pulseRunning) return;
    [self.haloLayer removeAnimationForKey:@"mimac.ready.pulse"];

    CAMediaTimingFunction *easeOut = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut];
    CAMediaTimingFunction *easeInOut = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseInEaseOut];
    CABasicAnimation *scale = [CABasicAnimation animationWithKeyPath:@"transform.scale"];
    scale.fromValue = @1.0;
    scale.toValue = @2.5;
    scale.duration = kMiMacReadyPulseDuration;
    scale.timingFunction = easeOut;

    CAKeyframeAnimation *opacity = [CAKeyframeAnimation animationWithKeyPath:@"opacity"];
    opacity.values = @[@0.10, @0.16, @0.0];
    opacity.keyTimes = @[@0.0, @0.25, @1.0];
    opacity.timingFunctions = @[easeInOut, easeInOut];
    opacity.duration = kMiMacReadyPulseDuration;

    CAAnimationGroup *pulse = [CAAnimationGroup animation];
    pulse.animations = @[scale, opacity];
    pulse.duration = kMiMacReadyPulseDuration;
    pulse.repeatCount = HUGE_VALF;
    pulse.beginTime = [self.haloLayer convertTime:CACurrentMediaTime() fromLayer:nil];
    pulse.fillMode = kCAFillModeBoth;
    pulse.removedOnCompletion = NO;

    [CATransaction begin];
    [CATransaction setDisableActions:YES];
    self.haloLayer.transform = CATransform3DMakeScale(2.5, 2.5, 1.0);
    self.haloLayer.opacity = 0;
    [self.haloLayer addAnimation:pulse forKey:@"mimac.ready.pulse"];
    [CATransaction commit];
    self.pulseRunning = YES;
}

- (void)stopPulseWithOpacity:(float)opacity {
    if (self.pulseRunning) [self.haloLayer removeAnimationForKey:@"mimac.ready.pulse"];
    self.pulseRunning = NO;
    [CATransaction begin];
    [CATransaction setDisableActions:YES];
    self.haloLayer.transform = CATransform3DIdentity;
    self.haloLayer.opacity = self.hasPlaceholder ? opacity : 0;
    [CATransaction commit];
}

- (void)removeObservers {
    NSNotificationCenter *center = NSNotificationCenter.defaultCenter;
    if (self.occlusionObserver) [center removeObserver:self.occlusionObserver];
    if (self.displayOptionsObserver) {
        [NSWorkspace.sharedWorkspace.notificationCenter removeObserver:self.displayOptionsObserver];
    }
    self.occlusionObserver = nil;
    self.displayOptionsObserver = nil;
}

- (void)closeIndicator {
    if (self.closed) return;
    self.closed = YES;
    self.hasPlaceholder = NO;
    [self removeObservers];
    [self stopPulseWithOpacity:0];
    self.hidden = YES;
    [self removeFromSuperview];
}

- (void)dealloc {
    [self removeObservers];
}

@end

uint32_t MiMacReadyIndicatorABIVersion(void) { return 1; }

uintptr_t MiMacReadyIndicatorInstall(uintptr_t ns_window) {
    if (!NSThread.isMainThread || ns_window == 0) return 0;
    @autoreleasepool {
        NSWindow *window = (__bridge NSWindow *)(void *)ns_window;
        NSView *contentView = window.contentView;
        if (!contentView) return 0;
        MiMacReadyIndicatorOverlayView *overlay = [[MiMacReadyIndicatorOverlayView alloc] initWithFrame:contentView.bounds];
        [contentView addSubview:overlay positioned:NSWindowAbove relativeTo:nil];
        return (uintptr_t)(__bridge_retained void *)overlay;
    }
}

int32_t MiMacReadyIndicatorUpdate(uintptr_t handle,
                                  double x, double y, double width, double height,
                                  uint32_t rgba, int32_t animate) {
    if (!NSThread.isMainThread || handle == 0) return 0;
    @autoreleasepool {
        MiMacReadyIndicatorOverlayView *overlay = (__bridge MiMacReadyIndicatorOverlayView *)(void *)handle;
        return [overlay updateRect:CGRectMake(x, y, width, height)
                              rgba:rgba
                           animate:(animate != 0)] ? 1 : 0;
    }
}

void MiMacReadyIndicatorClose(uintptr_t handle) {
    if (!NSThread.isMainThread || handle == 0) return;
    @autoreleasepool {
        MiMacReadyIndicatorOverlayView *overlay = (__bridge_transfer MiMacReadyIndicatorOverlayView *)(void *)handle;
        [overlay closeIndicator];
    }
}
