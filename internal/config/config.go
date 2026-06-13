package config

type ManagerConfig struct {
	NodeName string
	APIAddr  string
	DataDir  string
}

type WorkerConfig struct {
	NodeName string
	Managers []string
	DataDir  string
	Limits   ResourceLimits
}

type ResourceLimits struct {
	CPUCores    int
	MemoryBytes uint64
	DiskBytes   uint64
	GPUEnabled  bool
	VRAMBytes   uint64
}
