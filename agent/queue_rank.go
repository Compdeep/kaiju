package agent

import "log"

// maxQueueRank bounds Config.QueueRank, so no rank can wrap the priority.
const maxQueueRank = 1000

/*
 * rankInQueue asks the application how far back a run waits.
 * desc: Config.QueueRank, held to 0..maxQueueRank. Nil, or a panic in the
 *       application's code, ranks the run 0: a run queues as it always did
 *       rather than not at all.
 * param: t - the run about to be queued.
 * return: the rank.
 */
func (a *Agent) rankInQueue(t Trigger) (rank int) {
	if a == nil || a.queueRank == nil {
		return 0
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[kernel] the queue rank panicked, queuing the run as ordinary: %v", r)
			rank = 0
		}
	}()
	rank = a.queueRank(t)
	if rank < 0 {
		return 0
	}
	if rank > maxQueueRank {
		return maxQueueRank
	}
	return rank
}
