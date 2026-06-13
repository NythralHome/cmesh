package scheduler

import (
	"errors"

	"github.com/cmesh/cmesh/internal/cluster"
	"github.com/cmesh/cmesh/internal/jobs"
)

var ErrNoEligibleWorker = errors.New("no eligible worker")

type Scheduler struct{}

func New() *Scheduler {
	return &Scheduler{}
}

func (s *Scheduler) PickWorker(job jobs.Job, workers []cluster.Node) (cluster.Node, error) {
	for _, worker := range workers {
		if worker.Role == cluster.NodeRoleWorker && worker.Status == cluster.NodeStatusOnline {
			return worker, nil
		}
	}

	return cluster.Node{}, ErrNoEligibleWorker
}
