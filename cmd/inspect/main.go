package main

import (
	"flag"
	"fmt"
	"golang.org/x/net/context"
	"os"
	"time"

	v1 "k8s.io/api/core/v1"
)

const (
	resourceName         = "aliyun.com/gpu-mem"
	countName            = "aliyun.com/gpu-count"
	gpuCountKey          = "aliyun.accelerator/nvidia_count"
	cardNameKey          = "aliyun.accelerator/nvidia_name"
	gpuMemKey            = "aliyun.accelerator/nvidia_mem"
	pluginComponentKey   = "component"
	pluginComponentValue = "gpushare-device-plugin"

	envNVGPUID             = "ALIYUN_COM_GPU_MEM_IDX"
	envPodGPUMemory        = "ALIYUN_COM_GPU_MEM_POD"
	envTOTALGPUMEMORY      = "ALIYUN_COM_GPU_MEM_DEV"
	gpushareAllocationFlag = "scheduler.framework.gpushare.allocation"
)

func init() {
	kubeInit()
	// checkpointInit()
}

func main() {
	var nodeName string
	// nodeName := flag.String("nodeName", "", "nodeName")
	details := flag.Bool("d", false, "details")
	flag.Parse()

	args := flag.Args()
	if len(args) > 0 {
		nodeName = args[0]
	}

	var pods []v1.Pod
	var nodes []v1.Node
	var err error

	ctx, cancel := context.WithTimeout(context.TODO(), 15*time.Second)
	defer cancel()

	if nodeName == "" {
		nodes, err = getAllSharedGPUNode(ctx)
		if err == nil {
			pods, err = getActivePodsInAllNodes(ctx)
		}
	} else {
		nodes, err = getNodes(ctx, nodeName)
		if err == nil {
			pods, err = getActivePodsByNode(ctx, nodeName)
		}
	}

	if err != nil {
		fmt.Printf("Failed due to %v", err)
		os.Exit(1)
	}

	nodeInfos, err := buildAllNodeInfos(pods, nodes)
	if err != nil {
		fmt.Printf("Failed due to %v", err)
		os.Exit(1)
	}
	if *details {
		displayDetails(nodeInfos)
	} else {
		displaySummary(nodeInfos)
	}

}
