package workflowrunner

import (
	"context"
	"errors"
	"net/http"
	"time"

	"cs-cloud/internal/logger"
)

const outboxDeliveryInterval = 10 * time.Second

// startOutboxDelivery runs a background loop that drains pending task facts
// from the durable outbox to the server. It performs an initial scan
// immediately, then waits outboxDeliveryInterval between subsequent passes.
// The loop returns when ctx is cancelled. Delivery failures are logged and
// retried; the loop never exits because of a failed fact.
func (d *Driver) startOutboxDelivery(ctx context.Context) {
	if d.outbox == nil {
		logger.Warn("workflow: outbox delivery started without an outbox")
		return
	}

	ticker := time.NewTicker(outboxDeliveryInterval)
	defer ticker.Stop()

	for {
		d.deliverOutboxPass(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// deliverOutboxPass attempts to deliver every currently pending fact once.
// Accepted facts (2xx) and already-finalized facts (409) are moved to done.
// Transient failures are logged and the fact's attempts counter is persisted
// so backoff/retry can be implemented later.
func (d *Driver) deliverOutboxPass(ctx context.Context) {
	facts, err := d.outbox.Pending()
	if err != nil {
		logger.Warn("workflow: outbox pending scan failed: %v", err)
		return
	}
	if len(facts) == 0 {
		return
	}

	for _, f := range facts {
		delivered, err := d.deliverOutboxFact(ctx, f)
		if err != nil {
			logger.Warn("workflow: outbox delivery for fact %s failed: %v", f.FactID, err)
		}
		if delivered {
			if err := d.outbox.MarkDone(f.FactID); err != nil {
				logger.Warn("workflow: outbox mark done for fact %s failed: %v", f.FactID, err)
			}
			continue
		}
		f.Attempts++
		if err := d.outbox.Add(f); err != nil {
			logger.Warn("workflow: outbox increment attempts for fact %s failed: %v", f.FactID, err)
		}
	}
}

// deliverOutboxFact posts a single fact to the server. It returns true when
// the fact can be considered delivered (2xx or 409 already-finalized).
func (d *Driver) deliverOutboxFact(ctx context.Context, f OutboxFact) (bool, error) {
	if d.client == nil {
		return false, errors.New("no client")
	}
	err := d.client.PostTaskFact(ctx, f)
	if err == nil {
		return true, nil
	}
	var stErr *StatusError
	if errors.As(err, &stErr) && stErr.StatusCode == http.StatusConflict {
		// Server reports the task is already in a terminal state; the outcome
		// is already recorded, so drop the local fact.
		return true, nil
	}
	return false, err
}
