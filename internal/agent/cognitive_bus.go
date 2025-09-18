package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// CognitiveMessage is the envelope for data transmitted over the CognitiveBus.
type CognitiveMessage struct {
	ID        string
	Timestamp time.Time
	Type      CognitiveMessageType
	Payload   interface{}
}

// CognitiveBus manages the flow of information using a Pub/Sub model.
// Implements blocking sends (backpressure) and graceful shutdown.
type CognitiveBus struct {
	logger *zap.Logger

	// Map of message type to a list of channels (subscribers).
	subscribers map[CognitiveMessageType][]chan CognitiveMessage
	mu          sync.RWMutex
	bufferSize  int

	// WaitGroup to track messages currently being processed by consumers.
	// Matches the name used in the provided tests.
	processingWg sync.WaitGroup
	// WaitGroup to track active Post operations.
	activePostsWg sync.WaitGroup

	// Shutdown mechanism
	isShutdown bool
	shutdownMu sync.Mutex
}

// NewCognitiveBus initializes the CognitiveBus.
func NewCognitiveBus(logger *zap.Logger, bufferSize int) *CognitiveBus {
	if bufferSize <= 0 {
		bufferSize = 100
	}
	return &CognitiveBus{
		logger:      logger.Named("cognitive_bus"),
		subscribers: make(map[CognitiveMessageType][]chan CognitiveMessage),
		bufferSize:  bufferSize,
	}
}

// Post sends a message onto the bus. Blocks if subscriber buffers are full.
func (cb *CognitiveBus) Post(ctx context.Context, msg CognitiveMessage) (err error) {
	// 1. Check shutdown state and increment activePostsWg.
	cb.shutdownMu.Lock()
	if cb.isShutdown {
		cb.shutdownMu.Unlock()
		return fmt.Errorf("cannot post message: CognitiveBus is shut down")
	}
	cb.activePostsWg.Add(1)
	cb.shutdownMu.Unlock()
	// Ensure activePostsWg.Done() is called after the recover block finishes.
	defer cb.activePostsWg.Done()

	// Use a recover block to gracefully handle sends on channels closed during shutdown.
	defer func() {
		if r := recover(); r != nil {
			// A panic here means a message we incremented processingWg for (inside the loop)
			// was NOT successfully delivered (because the send panicked).
			// We must decrement the counter for this specific failed delivery to prevent a deadlock.
			cb.processingWg.Done()

			// This likely means a send on a closed channel occurred during shutdown.
			cb.logger.Debug("Recovered from panic in Post, likely due to shutdown.", zap.Any("panic", r))
			err = fmt.Errorf("failed to post message: bus is shutting down")
		}
	}()

	// 2. Enrich the message (required by tests).
	if msg.ID == "" {
		msg.ID = uuid.New().String()
	}
	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	// 3. Acquire read lock to access subscribers.
	cb.mu.RLock()

	// Start with subscribers specific to the message type.
	subscribers, ok := cb.subscribers[msg.Type]

	// Also check for "all" subscribers (empty string key).
	allSubscribers, allOk := cb.subscribers[""]

	if (!ok || len(subscribers) == 0) && (!allOk || len(allSubscribers) == 0) {
		cb.mu.RUnlock()
		return nil // No one is listening.
	}

	// Combine specific and "all" subscribers.
	combinedSubscribers := append(subscribers, allSubscribers...)

	// Create a copy of the subscribers slice to avoid holding the lock during channel sends.
	// We must ensure uniqueness if a subscriber listens to both specific and "all" types.
	uniqueSubs := make(map[chan CognitiveMessage]struct{})
	for _, sub := range combinedSubscribers {
		uniqueSubs[sub] = struct{}{}
	}

	subsCopy := make([]chan CognitiveMessage, 0, len(uniqueSubs))
	for ch := range uniqueSubs {
		subsCopy = append(subsCopy, ch)
	}

	cb.mu.RUnlock()

	// 4. Distribute the message, tracking each delivery.
	for _, ch := range subsCopy {
		// Increment the waitgroup for this specific delivery attempt.
		// This must happen BEFORE the select block, so the recover block can correctly call Done() if the send panics.
		cb.processingWg.Add(1)
		select {
		case ch <- msg:
		// Delivered successfully. The consumer is responsible for calling Acknowledge (which calls processingWg.Done()).
		case <-ctx.Done():
			// Delivery failed due to context cancellation, so undo the Add.
			cb.processingWg.Done()
			return ctx.Err()
		}
		// If ch <- msg panics (due to channel closed during shutdown), the top-level defer/recover handles it, calls processingWg.Done(), and sets the error.
	}
	return nil
}

// Subscribe returns a channel to listen for specific message types.
// Supports variadic arguments to align with test usage (e.g. Subscribe(TypeA, TypeB)).
func (cb *CognitiveBus) Subscribe(msgTypes ...CognitiveMessageType) (<-chan CognitiveMessage, func()) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	ch := make(chan CognitiveMessage, cb.bufferSize)

	// If no types provided, subscribe to all known types
	if len(msgTypes) == 0 {
		msgTypes = []CognitiveMessageType{""} // Empty string for "all"
	}

	// Keep track of the types this specific channel is subscribed to for unsubscription logic.
	subscribedTypes := make([]CognitiveMessageType, len(msgTypes))
	copy(subscribedTypes, msgTypes)

	for _, msgType := range subscribedTypes {
		cb.subscribers[msgType] = append(cb.subscribers[msgType], ch)
	}

	unsubscribe := func() {
		cb.mu.Lock()
		defer cb.mu.Unlock()

		if cb.isShutdown {
			return
		}

		// Remove the channel from all subscribed types it was associated with.
		for _, msgType := range subscribedTypes {
			subs, exists := cb.subscribers[msgType]
			if !exists {
				continue
			}
			for i, subscriberCh := range subs {
				if subscriberCh == ch {
					// Efficient removal (order doesn't necessarily matter)
					copy(subs[i:], subs[i+1:])
					cb.subscribers[msgType] = subs[:len(subs)-1]

					// Optimization: Clean up empty subscriber lists.
					if len(cb.subscribers[msgType]) == 0 {
						delete(cb.subscribers, msgType)
					}
					break // Found in this list, move to the next type
				}
			}
		}
		// The channel should not be closed here by the unsubscriber,
		// but by the Shutdown method to prevent panics on Post.
	}

	return ch, unsubscribe
}

// Acknowledge signals that a message has been processed by a consumer.
func (cb *CognitiveBus) Acknowledge(msg CognitiveMessage) {
	cb.processingWg.Done()
}

// Shutdown gracefully closes the bus, waiting for all messages to be acknowledged.
func (cb *CognitiveBus) Shutdown() {
	// 1. Set shutdown flag to prevent new posts from starting.
	cb.shutdownMu.Lock()
	if cb.isShutdown {
		cb.shutdownMu.Unlock()
		return
	}
	cb.isShutdown = true
	cb.shutdownMu.Unlock()

	// We must close the channels BEFORE waiting for active posts (activePostsWg) to drain.
	// When the channels close, blocked Post operations will unblock (by panicking and recovering).

	// 2. Close all subscriber channels under a write lock.
	// This signals subscribers and unblocks blocked Post operations.
	cb.mu.Lock()
	uniqueChannels := make(map[chan CognitiveMessage]struct{})
	for _, subs := range cb.subscribers {
		for _, ch := range subs {
			uniqueChannels[ch] = struct{}{}
		}
	}
	for ch := range uniqueChannels {
		close(ch)
	}
	cb.subscribers = make(map[CognitiveMessageType][]chan CognitiveMessage)
	cb.mu.Unlock()

	// 3. Wait for any Post calls that were in-flight (and potentially blocked) to finish their logic (including their recover blocks).
	cb.activePostsWg.Wait()

	// 4. Wait for any successfully delivered messages (before the channels were closed) to be acknowledged.
	cb.processingWg.Wait()
}