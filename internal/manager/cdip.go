package manager

import (
	"fmt"

	"github.com/cmesh/cmesh/internal/cdip"
	"github.com/cmesh/cmesh/internal/models"
)

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

func cdipShardManifest(model models.Model, plan DistributedModelPlan) cdip.ShardManifest {
	shards := make([]cdip.ModelShard, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		shards = append(shards, cdip.ModelShard{
			Stage: cdip.Stage{
				Index:      stage.Index,
				NodeID:     stage.NodeID,
				NodeName:   stage.NodeName,
				LayerStart: stage.LayerStart,
				LayerEnd:   stage.LayerEnd,
			},
			Runtime:             plan.Runtime,
			RequiredMemoryBytes: stage.MemoryBytes,
			RequiredDiskBytes:   stage.DiskBytes,
			SourceArtifact:      model.URL,
			TargetArtifact:      fmt.Sprintf("%s.stage-%d.layers-%d-%d", model.ID, stage.Index, stage.LayerStart, stage.LayerEnd),
			Materialization:     cdip.ShardLogicalLayers,
			Capabilities: []string{
				"pipeline-stage-prepare",
				"pipeline-prefill",
				"pipeline-decode",
				"activation-stream-v1",
			},
		})
	}
	return cdip.ShardManifest{
		Envelope: cdip.NewEnvelope(cdip.MessageShardManifest),
		Model: cdip.ModelArtifact{
			ModelID:    model.ID,
			Runtime:    string(model.Runtime),
			Repository: model.Repo,
			File:       model.File,
			URL:        model.URL,
			Quant:      model.Quant,
			Parameters: model.Parameters,
			Bytes:      model.DiskBytes,
		},
		Mode:            plan.Mode,
		TotalLayers:     plan.TotalLayers,
		Materialization: cdip.ShardLogicalLayers,
		Shards:          shards,
		Warnings: []string{
			"logical layer split only; physical GGUF shard materialization is not implemented yet",
		},
	}
}
