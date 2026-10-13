package runner

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/jmurray2011/testease/internal/record"
	"github.com/jmurray2011/testease/internal/scenario"
	"github.com/jmurray2011/testease/internal/target"
)

type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

func (r *run) carry(ctx context.Context, step scenario.Step, source target.Session, stdout io.Writer) (target.Result, []record.Fact, error) {
	started := time.Now()
	result := target.Result{RC: -1}
	if step.Timeout != 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(step.Timeout))
		defer cancel()
	}

	body, err := source.Fetch(ctx, step.Carry.From)
	if err != nil {
		result.Duration = time.Since(started)
		return result, nil, fmt.Errorf("fetch carry source %s:%s: %w", step.Host, step.Carry.From, err)
	}
	defer func() { _ = body.Close() }()

	hash := sha256.New()
	count := &byteCounter{}
	stream := io.TeeReader(body, io.MultiWriter(hash, count))
	seededBeforeStart := false
	if receiver, ok := r.targets[step.Carry.ToHost].(target.CarryReceiver); ok {
		receipt, receiveErr := receiver.ReceiveCarry(ctx, stream, step.Carry.To)
		if receiveErr != nil {
			result.Duration = time.Since(started)
			return result, nil, fmt.Errorf("seed carry destination %s:%s: %w", step.Carry.ToHost, step.Carry.To, receiveErr)
		}
		seededBeforeStart = receipt.SeededBeforeStart
	} else {
		destination, sessionErr := r.session(ctx, step.Carry.ToHost)
		if sessionErr != nil {
			result.Duration = time.Since(started)
			return result, nil, sessionErr
		}
		if putErr := destination.Put(ctx, stream, step.Carry.To, 0o644); putErr != nil {
			result.Duration = time.Since(started)
			return result, nil, fmt.Errorf("put carry destination %s:%s: %w", step.Carry.ToHost, step.Carry.To, putErr)
		}
	}

	digest := fmt.Sprintf("sha256:%x", hash.Sum(nil))
	_, _ = fmt.Fprintf(stdout, "carried %d bytes from %s:%s to %s:%s (%s)\n",
		count.n, step.Host, step.Carry.From, step.Carry.ToHost, step.Carry.To, digest)
	result.RC = 0
	result.Duration = time.Since(started)
	return result, carryFacts(step, count.n, digest, seededBeforeStart), nil
}

func carryFacts(step scenario.Step, bytes int64, digest string, seededBeforeStart bool) []record.Fact {
	values := map[string]string{
		"carry.source_host":         step.Host,
		"carry.source_path":         step.Carry.From,
		"carry.destination_host":    step.Carry.ToHost,
		"carry.destination_path":    step.Carry.To,
		"carry.bytes":               fmt.Sprint(bytes),
		"carry.sha256":              digest,
		"carry.seeded_before_start": map[bool]string{true: "yes", false: "no"}[seededBeforeStart],
	}
	facts := make([]record.Fact, 0, len(values))
	for _, key := range slices.Sorted(maps.Keys(values)) {
		value := values[key]
		facts = append(facts, record.Fact{Key: key, Value: &value, Probe: "runner:carry"})
	}
	return facts
}
