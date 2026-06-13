package storage

type ArtifactKind string

const (
	ArtifactModel     ArtifactKind = "model"
	ArtifactTokenizer ArtifactKind = "tokenizer"
	ArtifactInput     ArtifactKind = "input"
	ArtifactOutput    ArtifactKind = "output"
	ArtifactBenchmark ArtifactKind = "benchmark"
)

type Artifact struct {
	ID          string
	Kind        ArtifactKind
	Name        string
	SizeBytes   uint64
	SHA256      string
	Locations   []ArtifactLocation
	ContentType string
}

type ArtifactLocation struct {
	NodeID string
	Path   string
}
