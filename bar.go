package mpb

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/vbauerster/mpb/decor"
)

const (
	rLeft = iota
	rFill
	rTip
	rEmpty
	rRight
)

const (
	formatLen = 5
	etaAlpha  = 0.12
)

type barRunes [formatLen]rune

// Bar represents a progress Bar
type Bar struct {
	priority int
	index    int

	runningBar    *Bar
	cacheState    *bState
	operateState  chan func(*bState)
	frameReaderCh chan io.Reader

	// done is closed by Bar's goroutine, after cacheState is written
	done chan struct{}
	// shutdown is closed from master Progress goroutine only
	shutdown chan struct{}
}

type (
	bState struct {
		id                   int
		width                int
		runes                barRunes
		etaAlpha             float64
		total                int64
		current              int64
		totalAutoIncrTrigger int64
		totalAutoIncrBy      int64
		trimLeftSpace        bool
		trimRightSpace       bool
		toComplete           bool
		dynamic              bool
		removeOnComplete     bool
		barClearOnComplete   bool
		completeFlushed      bool
		startTime            time.Time
		blockStartTime       time.Time
		timeElapsed          time.Duration
		timePerItemEstimate  time.Duration
		timeRemaining        time.Duration
		aDecorators          []decor.DecoratorFunc
		pDecorators          []decor.DecoratorFunc
		refill               *refill
		bufP, bufB, bufA     *bytes.Buffer
		panicMsg             string

		// following options are assigned to the *Bar
		priority   int
		runningBar *Bar
	}
	refill struct {
		char rune
		till int64
	}
	frameReader struct {
		io.Reader
		toShutdown       bool
		removeOnComplete bool
	}
)

func newBar(wg *sync.WaitGroup, id int, total int64, cancel <-chan struct{}, options ...BarOption) *Bar {
	dynamic := total <= 0
	if dynamic {
		total = time.Now().Unix()
	}

	s := &bState{
		id:       id,
		priority: id,
		total:    total,
		etaAlpha: etaAlpha,
		dynamic:  dynamic,
	}

	for _, opt := range options {
		if opt != nil {
			opt(s)
		}
	}

	s.bufP = bytes.NewBuffer(make([]byte, 0, s.width))
	s.bufB = bytes.NewBuffer(make([]byte, 0, s.width))
	s.bufA = bytes.NewBuffer(make([]byte, 0, s.width))

	b := &Bar{
		priority:      s.priority,
		runningBar:    s.runningBar,
		operateState:  make(chan func(*bState)),
		frameReaderCh: make(chan io.Reader, 1),
		done:          make(chan struct{}),
		shutdown:      make(chan struct{}),
	}

	if b.runningBar != nil {
		b.priority = b.runningBar.priority
	}

	go b.serve(wg, s, cancel)
	return b
}

// RemoveAllPrependers removes all prepend functions
func (b *Bar) RemoveAllPrependers() {
	select {
	case b.operateState <- func(s *bState) { s.pDecorators = nil }:
	case <-b.done:
	}
}

// RemoveAllAppenders removes all append functions
func (b *Bar) RemoveAllAppenders() {
	select {
	case b.operateState <- func(s *bState) { s.aDecorators = nil }:
	case <-b.done:
	}
}

// ProxyReader wrapper for io operations, like io.Copy
func (b *Bar) ProxyReader(r io.Reader) *Reader {
	return &Reader{r, b}
}

// Increment is a shorthand for b.IncrBy(1)
func (b *Bar) Increment() {
	b.IncrBy(1)
}

// ResumeFill fills bar with different r rune,
// from 0 to till amount of progress.
func (b *Bar) ResumeFill(r rune, till int64) {
	if till < 1 {
		return
	}
	select {
	case b.operateState <- func(s *bState) { s.refill = &refill{r, till} }:
	case <-b.done:
	}
}

// NumOfAppenders returns current number of append decorators
func (b *Bar) NumOfAppenders() int {
	result := make(chan int)
	select {
	case b.operateState <- func(s *bState) { result <- len(s.aDecorators) }:
		return <-result
	case <-b.done:
		return len(b.cacheState.aDecorators)
	}
}

// NumOfPrependers returns current number of prepend decorators
func (b *Bar) NumOfPrependers() int {
	result := make(chan int)
	select {
	case b.operateState <- func(s *bState) { result <- len(s.pDecorators) }:
		return <-result
	case <-b.done:
		return len(b.cacheState.pDecorators)
	}
}

// ID returs id of the bar
func (b *Bar) ID() int {
	result := make(chan int)
	select {
	case b.operateState <- func(s *bState) { result <- s.id }:
		return <-result
	case <-b.done:
		return b.cacheState.id
	}
}

// Current returns bar's current number, in other words sum of all increments.
func (b *Bar) Current() int64 {
	result := make(chan int64)
	select {
	case b.operateState <- func(s *bState) { result <- s.current }:
		return <-result
	case <-b.done:
		return b.cacheState.current
	}
}

// Total returns bar's total number.
func (b *Bar) Total() int64 {
	result := make(chan int64)
	select {
	case b.operateState <- func(s *bState) { result <- s.total }:
		return <-result
	case <-b.done:
		return b.cacheState.total
	}
}

// SetTotal sets total dynamically. The final param indicates the very last set,
// in other words you should set it to true when total is determined.
func (b *Bar) SetTotal(total int64, final bool) {
	select {
	case b.operateState <- func(s *bState) {
		if total != 0 {
			s.total = total
		}
		s.dynamic = !final
	}:
	case <-b.done:
	}
}

// StartBlock updates start timestamp of the current increment block.
// It is optional to call, unless ETA decorator is used.
// If *bar.ProxyReader is used, it will be called implicitly.
func (b *Bar) StartBlock() {
	now := time.Now()
	select {
	case b.operateState <- func(s *bState) {
		if s.current == 0 {
			s.startTime = now
		}
		s.blockStartTime = now
	}:
	case <-b.done:
	}
}

// IncrBy increments progress bar by amount of n
func (b *Bar) IncrBy(n int) {
	now := time.Now()
	select {
	case b.operateState <- func(s *bState) {
		s.current += int64(n)
		s.timeElapsed = now.Sub(s.startTime)
		s.timeRemaining = s.calcETA(n, now.Sub(s.blockStartTime))
		if s.dynamic {
			curp := decor.CalcPercentage(s.total, s.current, 100)
			if 100-curp <= s.totalAutoIncrTrigger {
				s.total += s.totalAutoIncrBy
			}
		} else if s.current >= s.total {
			s.current = s.total
			s.toComplete = true
		}
	}:
	case <-b.done:
	}
}

// Completed reports whether the bar is in completed state
func (b *Bar) Completed() bool {
	result := make(chan bool)
	select {
	case b.operateState <- func(s *bState) { result <- s.toComplete }:
		return <-result
	case <-b.done:
		return b.cacheState.toComplete
	}
}

func (b *Bar) serve(wg *sync.WaitGroup, s *bState, cancel <-chan struct{}) {
	defer wg.Done()
	s.startTime = time.Now()
	s.blockStartTime = s.startTime
	for {
		select {
		case op := <-b.operateState:
			op(s)
		case <-cancel:
			s.toComplete = true
			cancel = nil
		case <-b.shutdown:
			b.cacheState = s
			close(b.done)
			return
		}
	}
}

func (b *Bar) render(debugOut io.Writer, tw int, pSyncer, aSyncer *widthSyncer) {
	var r io.Reader
	select {
	case b.operateState <- func(s *bState) {
		defer func() {
			// recovering if external decorators panic
			if p := recover(); p != nil {
				s.panicMsg = fmt.Sprintf("panic: %v", p)
				s.pDecorators = nil
				s.aDecorators = nil
				s.toComplete = true
				// truncate panic msg to one tw line, if necessary
				r = strings.NewReader(fmt.Sprintf(fmt.Sprintf("%%.%ds\n", tw), s.panicMsg))
				fmt.Fprintf(debugOut, "%s %s bar id %02d %v\n", "[mpb]", time.Now(), s.id, s.panicMsg)
			}
			b.frameReaderCh <- &frameReader{
				Reader:           r,
				toShutdown:       s.toComplete && !s.completeFlushed,
				removeOnComplete: s.removeOnComplete,
			}
			s.completeFlushed = s.toComplete
		}()
		r = s.draw(tw, pSyncer, aSyncer)
	}:
	case <-b.done:
		s := b.cacheState
		if s.panicMsg != "" {
			r = strings.NewReader(fmt.Sprintf(fmt.Sprintf("%%.%ds\n", tw), s.panicMsg))
		} else {
			r = s.draw(tw, pSyncer, aSyncer)
		}
		b.frameReaderCh <- &frameReader{
			Reader: r,
		}
	}
}

func (s *bState) draw(termWidth int, pSyncer, aSyncer *widthSyncer) io.Reader {
	defer s.bufA.WriteByte('\n')

	if termWidth <= 0 {
		termWidth = s.width
	}

	stat := newStatistics(s)

	// render prepend functions to the left of the bar
	for i, f := range s.pDecorators {
		s.bufP.WriteString(f(stat, pSyncer.Accumulator[i], pSyncer.Distributor[i]))
	}

	for i, f := range s.aDecorators {
		s.bufA.WriteString(f(stat, aSyncer.Accumulator[i], aSyncer.Distributor[i]))
	}

	prependCount := utf8.RuneCount(s.bufP.Bytes())
	appendCount := utf8.RuneCount(s.bufA.Bytes())

	if s.barClearOnComplete && s.completeFlushed {
		return io.MultiReader(s.bufP, s.bufA)
	}

	s.fillBar(s.width)
	barCount := utf8.RuneCount(s.bufB.Bytes())
	totalCount := prependCount + barCount + appendCount
	if spaceCount := 0; totalCount > termWidth {
		if !s.trimLeftSpace {
			spaceCount++
		}
		if !s.trimRightSpace {
			spaceCount++
		}
		s.fillBar(termWidth - prependCount - appendCount - spaceCount)
	}

	return io.MultiReader(s.bufP, s.bufB, s.bufA)
}

func (s *bState) fillBar(width int) {
	defer func() {
		s.bufB.WriteRune(s.runes[rRight])
		if !s.trimRightSpace {
			s.bufB.WriteByte(' ')
		}
	}()

	s.bufB.Reset()
	if !s.trimLeftSpace {
		s.bufB.WriteByte(' ')
	}
	s.bufB.WriteRune(s.runes[rLeft])
	if width <= 2 {
		return
	}

	// bar s.width without leftEnd and rightEnd runes
	barWidth := width - 2

	completedWidth := decor.CalcPercentage(s.total, s.current, int64(barWidth))

	if s.refill != nil {
		till := decor.CalcPercentage(s.total, s.refill.till, int64(barWidth))
		// append refill rune
		var i int64
		for i = 0; i < till; i++ {
			s.bufB.WriteRune(s.refill.char)
		}
		for i = till; i < completedWidth; i++ {
			s.bufB.WriteRune(s.runes[rFill])
		}
	} else {
		var i int64
		for i = 0; i < completedWidth; i++ {
			s.bufB.WriteRune(s.runes[rFill])
		}
	}

	if completedWidth < int64(barWidth) && completedWidth > 0 {
		_, size := utf8.DecodeLastRune(s.bufB.Bytes())
		s.bufB.Truncate(s.bufB.Len() - size)
		s.bufB.WriteRune(s.runes[rTip])
	}

	for i := completedWidth; i < int64(barWidth); i++ {
		s.bufB.WriteRune(s.runes[rEmpty])
	}
}

func (s *bState) calcETA(n int, lastBlockTime time.Duration) time.Duration {
	lastItemEstimate := float64(lastBlockTime) / float64(n)
	s.timePerItemEstimate = time.Duration((s.etaAlpha * lastItemEstimate) + (1-s.etaAlpha)*float64(s.timePerItemEstimate))
	return time.Duration(s.total-s.current) * s.timePerItemEstimate
}

func newStatistics(s *bState) *decor.Statistics {
	return &decor.Statistics{
		ID:                  s.id,
		Completed:           s.completeFlushed,
		Total:               s.total,
		Current:             s.current,
		StartTime:           s.startTime,
		TimeElapsed:         s.timeElapsed,
		TimeRemaining:       s.timeRemaining,
		TimePerItemEstimate: s.timePerItemEstimate,
	}
}

func strToBarRunes(format string) (array barRunes) {
	for i, n := 0, 0; len(format) > 0; i++ {
		array[i], n = utf8.DecodeRuneInString(format)
		format = format[n:]
	}
	return
}
