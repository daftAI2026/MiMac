package main

import "context"

// This preparation decorator adds no session state. The existing prepared
// resource owner closes partial resources if either preparation step fails.
type compatControlPreparedSink struct {
	xiaomiPreparedSink
	prepareControl func(context.Context) error
}

func (s *compatControlPreparedSink) Prepare(ctx context.Context) error {
	if ctx == nil {
		return errCompatRouteContext
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.xiaomiPreparedSink.Prepare(ctx); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.prepareControl(ctx)
}
