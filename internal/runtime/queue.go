package runtime

import (
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// QueueMode is how many queued messages one drain point takes — Pi's
// QueueMode (packages/agent/src/types.ts at the pin).
type QueueMode string

const (
	// QueueOneAtATime takes only the oldest queued message, leaving the rest
	// for later drain points. Pi's default for both queues.
	QueueOneAtATime QueueMode = "one-at-a-time"

	// QueueAll takes every queued message at once.
	QueueAll QueueMode = "all"
)

// ParseQueueMode reads a mode, naming both when it cannot.
func ParseQueueMode(raw string) (QueueMode, error) {
	switch QueueMode(raw) {
	case QueueOneAtATime, QueueAll:
		return QueueMode(raw), nil
	}
	return "", fmt.Errorf("runtime: %q is not a queue mode; it takes all or one-at-a-time", raw)
}

func (m QueueMode) orDefault() QueueMode {
	if m == "" {
		return QueueOneAtATime
	}
	return m
}

// queueKind is which queue a pushed message belongs to. The framework keeps
// one buffer for everything pushed into a run, and Pi keeps two queues with
// two drain points, so which is which is tracked here, by message identity.
type queueKind int

const (
	queuePrompt queueKind = iota
	queueSteer
	queueFollow
	// queueMarker is not a message. It exists to preempt a turn, so that a
	// steering message left queued by one-at-a-time is taken at the next
	// drain point, and it is dropped when it reaches GenInput.
	queueMarker
)

// queues holds the two modes and the identity of everything queued.
type queues struct {
	mu       sync.Mutex
	steering QueueMode
	followUp QueueMode
	kinds    map[*schema.Message]queueKind

	// rearm is set when steering messages were left queued, and taken by the
	// next model request of the run, which pushes a marker to preempt it.
	rearm bool
	loop  *adk.TurnLoop[*schema.Message, *schema.Message]
}

func newQueues(steering, followUp QueueMode) *queues {
	return &queues{steering: steering.orDefault(), followUp: followUp.orDefault(),
		kinds: map[*schema.Message]queueKind{}}
}

func (q *queues) tag(m *schema.Message, kind queueKind) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.kinds[m] = kind
}

func (q *queues) modes() (steering, followUp QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.steering, q.followUp
}

func (q *queues) setSteering(m QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.steering = m.orDefault()
}

func (q *queues) setFollowUp(m QueueMode) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.followUp = m.orDefault()
}

func (q *queues) attach(loop *adk.TurnLoop[*schema.Message, *schema.Message]) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.loop = loop
	q.rearm = false
}

// split decides what one drain point takes, the way Pi's loop does: steering
// first, at every point it is queued, and follow-ups only when there is no
// steering — which is the point the run would otherwise stop. Each queue
// gives up one message or all of them according to its mode. What is not
// taken stays queued, in order, steering ahead of follow-ups.
func (q *queues) split(items []*schema.Message) (consumed, remaining []*schema.Message) {
	q.mu.Lock()
	defer q.mu.Unlock()

	var steer, follow []*schema.Message
	for _, item := range items {
		if item == nil {
			continue
		}
		kind := q.kinds[item]
		switch kind {
		case queueMarker:
			delete(q.kinds, item)
		case queueSteer:
			steer = append(steer, item)
		case queueFollow:
			follow = append(follow, item)
		default:
			consumed = append(consumed, item)
		}
	}

	take := func(from []*schema.Message, mode QueueMode) (taken, left []*schema.Message) {
		if mode == QueueAll || len(from) <= 1 {
			return from, nil
		}
		return from[:1], from[1:]
	}

	if len(steer) > 0 {
		taken, left := take(steer, q.steering)
		consumed = append(consumed, taken...)
		remaining = append(append(remaining, left...), follow...)
		// Left in the buffer, a steering message would wait for the run to
		// stop, which is a follow-up's drain point, not its own. The next
		// model request re-arms the preempt that takes it after that turn.
		q.rearm = len(left) > 0
	} else {
		taken, left := take(follow, q.followUp)
		consumed = append(consumed, taken...)
		remaining = left
	}
	for _, item := range consumed {
		delete(q.kinds, item)
	}
	return consumed, remaining
}

// rearmIfQueued is called as a turn makes a model request. When steering
// messages were left queued, it pushes a marker that preempts this turn
// after its tool round — Pi's next drain point — so the next one is taken
// there rather than when the run stops. A reply with no tool calls ends the
// turn without the preempt, and the queued message is taken then instead.
func (q *queues) rearmIfQueued() {
	q.mu.Lock()
	if !q.rearm || q.loop == nil {
		q.mu.Unlock()
		return
	}
	q.rearm = false
	loop := q.loop
	marker := schema.UserMessage("")
	q.kinds[marker] = queueMarker
	q.mu.Unlock()

	if ok, _ := loop.Push(marker, adk.WithPreempt[*schema.Message, *schema.Message](adk.AfterToolCalls)); !ok {
		q.mu.Lock()
		delete(q.kinds, marker)
		q.mu.Unlock()
	}
}
