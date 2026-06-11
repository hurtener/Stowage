package gateway

import (
	"context"
	"fmt"
	"sync"
	"time"
)

const (
	// BatchMaxSize is the maximum number of inputs in a single provider batch.
	BatchMaxSize = 64
	// BatchFlushInterval is the maximum time to wait before flushing a partial batch.
	BatchFlushInterval = 25 * time.Millisecond
)

// EmbedDoFunc is the provider-level function that executes a batch embed call.
type EmbedDoFunc func(ctx context.Context, inputs []string) ([][]float32, Usage, error)

type embedWork struct {
	input  string
	result chan embedResult
}

type embedResult struct {
	vec []float32
	err error
}

// Batcher coalesces concurrent single-input Embed calls into provider batches.
// A background goroutine drains the work channel every BatchFlushInterval or
// when BatchMaxSize items have accumulated. Exported so drivers can embed it.
type Batcher struct {
	work       chan embedWork
	doFn       EmbedDoFunc
	meter      Meter
	model      string
	once       sync.Once
	closeCh    chan struct{}
	loopWg     sync.WaitGroup
	dispatchWg sync.WaitGroup
}

// NewBatcher returns a started Batcher.
func NewBatcher(doFn EmbedDoFunc, meter Meter, model string) *Batcher {
	b := &Batcher{
		work:    make(chan embedWork, 512),
		doFn:    doFn,
		meter:   meter,
		model:   model,
		closeCh: make(chan struct{}),
	}
	b.loopWg.Add(1)
	go b.loop()
	return b
}

func (b *Batcher) loop() {
	defer b.loopWg.Done()
	ticker := time.NewTicker(BatchFlushInterval)
	defer ticker.Stop()

	var pending []embedWork

	flush := func() {
		if len(pending) == 0 {
			return
		}
		batch := pending
		pending = nil
		b.dispatchWg.Add(1)
		go func() {
			defer b.dispatchWg.Done()
			b.dispatch(batch)
		}()
	}

	for {
		select {
		case w, ok := <-b.work:
			if !ok {
				flush()
				return
			}
			pending = append(pending, w)
			if len(pending) >= BatchMaxSize {
				flush()
			}
		case <-ticker.C:
			flush()
		case <-b.closeCh:
			// drain remaining buffered work
			for {
				select {
				case w := <-b.work:
					pending = append(pending, w)
				default:
					goto drained
				}
			}
		drained:
			flush()
			return
		}
	}
}

func (b *Batcher) dispatch(batch []embedWork) {
	inputs := make([]string, len(batch))
	for i, w := range batch {
		inputs[i] = w.input
	}
	// The batcher dispatches asynchronously on behalf of multiple callers whose
	// contexts may have already been cancelled. Use Background context here.
	ctx := context.Background() //nolint:contextcheck
	vecs, usage, err := b.doFn(ctx, inputs)
	if err == nil && b.meter != nil {
		b.meter.Record(ctx, "embed", b.model, usage) //nolint:contextcheck
	}
	for i, w := range batch {
		if err != nil {
			w.result <- embedResult{err: err}
			continue
		}
		if i >= len(vecs) {
			w.result <- embedResult{err: fmt.Errorf(
				"gateway: provider returned %d vectors for %d inputs",
				len(vecs), len(inputs),
			)}
			continue
		}
		w.result <- embedResult{vec: vecs[i]}
	}
}

// Embed queues a single input for batching and blocks until the result is ready.
func (b *Batcher) Embed(ctx context.Context, input string) ([]float32, error) {
	w := embedWork{
		input:  input,
		result: make(chan embedResult, 1),
	}
	select {
	case b.work <- w:
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-b.closeCh:
		return nil, ErrGatewayUnavailable
	}
	select {
	case r := <-w.result:
		return r.vec, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Close signals the flush loop to drain and waits for all in-flight dispatches.
func (b *Batcher) Close() {
	b.once.Do(func() {
		close(b.closeCh)
		b.loopWg.Wait()
		b.dispatchWg.Wait()
	})
}
