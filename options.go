package mpb

import (
	"context"
	"io"
	"sync"
	"time"
)

// ContainerOption is a function option which changes the default
// behavior of progress container, if passed to mpb.New(...ContainerOption).
type ContainerOption func(*pState)

// WithWaitGroup provides means to have a single joint point. If
// *sync.WaitGroup is provided, you can safely call just p.Wait()
// without calling Wait() on provided *sync.WaitGroup. Makes sense
// when there are more than one bar to render.
func WithWaitGroup(wg *sync.WaitGroup) ContainerOption {
	return func(s *pState) {
		s.uwg = wg
	}
}

// WithWidth sets container width. Default is 80. Bars inherit this
// width, as long as no BarWidth is applied.
func WithWidth(w int) ContainerOption {
	return func(s *pState) {
		if w >= 0 {
			s.width = w
		}
	}
}

// WithRefreshRate overrides default 120ms refresh rate.
func WithRefreshRate(d time.Duration) ContainerOption {
	return func(s *pState) {
		if d < 10*time.Millisecond {
			return
		}
		s.rr = d
	}
}

// WithManualRefresh disables internal auto refresh time.Ticker.
// Refresh will occur upon receive value from provided ch.
func WithManualRefresh(ch <-chan time.Time) ContainerOption {
	return func(s *pState) {
		s.manualRefreshCh = ch
	}
}

// WithContext provided context will be used for cancellation purposes.
func WithContext(ctx context.Context) ContainerOption {
	return func(s *pState) {
		if ctx == nil {
			return
		}
		s.ctx = ctx
	}
}

// WithShutdownNotifier provided chanel will be closed, after all bars
// have been rendered.
func WithShutdownNotifier(ch chan struct{}) ContainerOption {
	return func(s *pState) {
		s.shutdownNotifier = ch
	}
}

// WithOutput overrides default output os.Stdout.
func WithOutput(w io.Writer) ContainerOption {
	return func(s *pState) {
		if w == nil {
			return
		}
		s.output = w
	}
}

// WithDebugOutput sets debug output.
func WithDebugOutput(w io.Writer) ContainerOption {
	return func(s *pState) {
		if w == nil {
			return
		}
		s.debugOut = w
	}
}

// ContainerOptOnCond returns option when condition evaluates to true.
func ContainerOptOnCond(option ContainerOption, condition func() bool) ContainerOption {
	if condition() {
		return option
	}
	return nil
}
