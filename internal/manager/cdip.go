package manager

import "github.com/cmesh/cmesh/internal/cdip"

func cdipPlanProposal(plan DistributedModelPlan) cdip.PlanProposal {
	stages := make([]cdip.Stage, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		stages = append(stages, cdip.Stage{
			Index:      stage.Index,
			NodeID:     stage.NodeID,
			NodeName:   stage.NodeName,
			LayerStart: stage.LayerStart,
			LayerEnd:   stage.LayerEnd,
		})
	}
	return cdip.PlanProposal{
		Envelope:      cdip.NewEnvelope(cdip.MessagePlanProposal),
		ModelID:       plan.ModelID,
		Mode:          plan.Mode,
		Runtime:       plan.Runtime,
		ExecutableNow: plan.ExecutableNow,
		Stages:        stages,
		Blockers:      append([]string(nil), plan.Blockers...),
	}
}
