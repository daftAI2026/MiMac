package main

import (
	"context"
	"errors"
	"sync"

	"mimac.local/mimac/internal/audiosession"
	"mimac.local/mimac/internal/bridge"
)

var (
	errLiveAudioFactoryNil = errors.New("live audio connection: factory is required")
	errLiveAudioRouteNil   = errors.New("live audio connection: factory returned no route")
	errLiveAudioClosed     = errors.New("live audio connection: connection is closing or closed")
	errLiveAudioNotReady   = errors.New("live audio connection: route is not ready")
)

// liveAudioRoute is the small lifecycle surface the connection needs from the
// host audio route. Tests can supply a fake without opening native devices.
type liveAudioRoute interface {
	Start() error
	Ready() bool
	Error() error
	Changes() <-chan struct{}
	Prepare(context.Context, bridge.SessionToken) (audiosession.Resources, error)
	Close(context.Context) error
}

// liveAudioConnection starts route construction and arming on a worker. The
// route and startup error are published by closing done; they are immutable
// after that point, so readers can use the channel as the publication barrier.
type liveAudioConnection struct {
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	closed chan struct{}

	route    liveAudioRoute
	startErr error

	stateMu   sync.Mutex
	closing   bool
	isClosed  bool
	closeGate chan struct{}
}

var (
	_ liveAudioRoute       = (*xiaomiAudioRoute)(nil)
	_ audiosession.Prepare = (*liveAudioConnection)(nil).Prepare
)

func newLiveAudioConnection(factory func(context.Context) (liveAudioRoute, error)) *liveAudioConnection {
	ctx, cancel := context.WithCancel(context.Background())
	connection := &liveAudioConnection{
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
		closed:    make(chan struct{}),
		closeGate: make(chan struct{}, 1),
	}
	go connection.prepare(factory)
	return connection
}

func (c *liveAudioConnection) prepare(factory func(context.Context) (liveAudioRoute, error)) {
	var route liveAudioRoute
	var err error
	if factory == nil {
		err = errLiveAudioFactoryNil
	} else {
		route, err = factory(c.ctx)
		if err == nil && route == nil {
			err = errLiveAudioRouteNil
		}
		if err == nil {
			if cancelled := c.ctx.Err(); cancelled != nil {
				err = cancelled
			} else {
				err = route.Start()
			}
		}
	}
	if cancelled := c.ctx.Err(); cancelled != nil {
		err = errors.Join(err, cancelled)
	}
	c.route = route
	c.startErr = err
	close(c.done)
}

// Done closes when the factory and route Start call have completed. It does
// not mean the route's asynchronous device preparation is ready.
func (c *liveAudioConnection) Done() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.done
}

// Closed closes only after Close has successfully cleaned the route. Failed
// cleanup leaves it open so callers can retry and continue watching changes.
func (c *liveAudioConnection) Closed() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.closed
}

// Changes forwards route preparation notifications after factory completion.
func (c *liveAudioConnection) Changes() <-chan struct{} {
	if c == nil {
		return nil
	}
	select {
	case <-c.done:
	default:
		return nil
	}
	c.stateMu.Lock()
	closed := c.isClosed
	route := c.route
	c.stateMu.Unlock()
	if closed || route == nil {
		return nil
	}
	return route.Changes()
}

// Ready is nonblocking and reports the route's asynchronous readiness only
// after factory creation and Start have completed successfully.
func (c *liveAudioConnection) Ready() bool {
	if c == nil {
		return false
	}
	select {
	case <-c.done:
	default:
		return false
	}
	c.stateMu.Lock()
	closing := c.closing || c.isClosed
	route, err := c.route, c.startErr
	c.stateMu.Unlock()
	return !closing && err == nil && route != nil && route.Ready()
}

// Err is nonblocking. Before Done it returns nil; afterward it reports a
// startup failure or the route's current asynchronous error.
func (c *liveAudioConnection) Err() error {
	if c == nil {
		return errLiveAudioRouteNil
	}
	select {
	case <-c.done:
	default:
		return nil
	}
	c.stateMu.Lock()
	route, err := c.route, c.startErr
	c.stateMu.Unlock()
	if err != nil {
		return err
	}
	if route == nil {
		return errLiveAudioRouteNil
	}
	return route.Error()
}

// BeforeStart runs a target adapter's start preflight after Core has
// admitted a new physical press, but before its first target key is posted.
// It never waits for construction or competes with native cleanup. Context
// checks bound admission between native calls, not an in-flight system call.
func (c *liveAudioConnection) BeforeStart(ctx context.Context) error {
	if ctx == nil {
		return errors.New("live audio connection: nil start context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c == nil {
		return errLiveAudioRouteNil
	}
	select {
	case c.closeGate <- struct{}{}:
	default:
		return errLiveAudioClosed
	}
	defer func() { <-c.closeGate }()
	if !c.Ready() {
		return errLiveAudioNotReady
	}
	if route, ok := c.route.(interface{ BeforeStart(context.Context) error }); ok {
		return route.BeforeStart(ctx)
	}
	return nil
}

// Prepare waits for construction and Start, then transfers the prepared
// resources from the route for this voice-session token.
func (c *liveAudioConnection) Prepare(ctx context.Context, token bridge.SessionToken) (audiosession.Resources, error) {
	if c == nil {
		return audiosession.Resources{}, errLiveAudioRouteNil
	}
	if ctx == nil {
		return audiosession.Resources{}, errors.New("live audio connection: nil prepare context")
	}
	if err := ctx.Err(); err != nil {
		return audiosession.Resources{}, err
	}
	c.stateMu.Lock()
	closing := c.closing || c.isClosed
	c.stateMu.Unlock()
	if closing {
		return audiosession.Resources{}, errLiveAudioClosed
	}
	select {
	case <-c.done:
	case <-ctx.Done():
		return audiosession.Resources{}, ctx.Err()
	}
	c.stateMu.Lock()
	closing = c.closing || c.isClosed
	route, err := c.route, c.startErr
	c.stateMu.Unlock()
	if closing {
		return audiosession.Resources{}, errLiveAudioClosed
	}
	if err != nil {
		return audiosession.Resources{}, err
	}
	if route == nil {
		return audiosession.Resources{}, errLiveAudioRouteNil
	}
	return route.Prepare(ctx, token)
}

// Close cancels startup, waits until its worker has published any partial
// owner, and then closes that route. A failed close keeps the route reachable
// and leaves Closed open so a later call can retry.
func (c *liveAudioConnection) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("live audio connection: nil close context")
	}
	select {
	case c.closeGate <- struct{}{}:
	case <-ctx.Done():
		return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
	}
	defer func() { <-c.closeGate }()
	if err := ctx.Err(); err != nil {
		return errors.Join(audiosession.ErrCleanupPending, err)
	}

	c.stateMu.Lock()
	if c.isClosed {
		c.stateMu.Unlock()
		return nil
	}
	c.closing = true
	c.stateMu.Unlock()
	c.cancel()

	select {
	case <-c.done:
	case <-ctx.Done():
		return errors.Join(audiosession.ErrCleanupPending, ctx.Err())
	}
	c.stateMu.Lock()
	route := c.route
	c.stateMu.Unlock()
	if route != nil {
		if err := route.Close(ctx); err != nil {
			return err
		}
	}
	c.stateMu.Lock()
	c.isClosed = true
	c.stateMu.Unlock()
	close(c.closed)
	return nil
}
