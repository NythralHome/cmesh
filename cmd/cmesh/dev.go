package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/cmesh/cmesh/internal/config"
)

func runDev(args []string) error {
	if len(args) == 0 {
		printDevUsage()
		return nil
	}

	switch args[0] {
	case "local-cluster":
		return runDevLocalCluster(args[1:])
	case "help", "--help", "-h":
		printDevUsage()
	default:
		return fmt.Errorf("unknown dev command %q", args[0])
	}

	return nil
}

func printDevUsage() {
	fmt.Println(`Usage:
  cmesh dev local-cluster    Register multiple local test workers`)
}

func runDevLocalCluster(args []string) error {
	fs := flag.NewFlagSet("dev local-cluster", flag.ContinueOnError)
	managerURL := fs.String("manager", "http://127.0.0.1:8080", "manager API URL")
	workers := fs.Int("workers", 3, "number of local test workers")
	benchmark := fs.Bool("benchmark", true, "run and submit benchmarks for each worker")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *workers <= 0 {
		return fmt.Errorf("workers must be greater than zero")
	}

	profiles := localWorkerProfiles(*workers)
	for _, profile := range profiles {
		options := workerOptions{
			managerURL:   strings.TrimRight(*managerURL, "/"),
			name:         profile.name,
			cacheDir:     defaultCacheDir() + "/" + profile.name,
			runBenchmark: *benchmark,
			runOnce:      true,
			limits: config.ResourceLimits{
				CPUCores:    profile.cpuCores,
				MemoryBytes: gbToBytes(profile.memoryGB),
				DiskBytes:   gbToBytes(profile.diskGB),
				GPUEnabled:  profile.gpu,
			},
		}

		if err := workerRun(context.Background(), options); err != nil {
			return err
		}
	}

	fmt.Printf("registered %d local test workers\n", len(profiles))
	return nil
}

type localWorkerProfile struct {
	name     string
	cpuCores int
	memoryGB uint64
	diskGB   uint64
	gpu      bool
}

func localWorkerProfiles(count int) []localWorkerProfile {
	base := []localWorkerProfile{
		{name: "local-small", cpuCores: 2, memoryGB: 2, diskGB: 10, gpu: false},
		{name: "local-medium", cpuCores: 4, memoryGB: 5, diskGB: 50, gpu: true},
		{name: "local-large", cpuCores: 8, memoryGB: 12, diskGB: 100, gpu: true},
	}

	profiles := make([]localWorkerProfile, 0, count)
	for i := 0; i < count; i++ {
		profile := base[i%len(base)]
		if i >= len(base) {
			profile.name = fmt.Sprintf("%s-%d", profile.name, i+1)
		}
		profiles = append(profiles, profile)
	}

	return profiles
}
