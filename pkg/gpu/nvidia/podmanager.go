package nvidia

import (
	"encoding/json"
	"fmt"
	"github.com/cjg/aliyun-gpushare-device-plugin/pkg/kubelet/client"
	log "github.com/golang/glog"
	"golang.org/x/net/context"
	"k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"os"
	"sort"
	"time"
)

var (
	clientset *kubernetes.Clientset
	nodeName  string
	retries   = 8
)

func kubeInit() {
	kubeconfigFile := os.Getenv("KUBECONFIG")
	var err error
	var config *rest.Config

	if _, err = os.Stat(kubeconfigFile); err != nil {
		log.V(5).Infof("kubeconfig %s failed to find due to %v", kubeconfigFile, err)
		config, err = rest.InClusterConfig()
		if err != nil {
			log.Fatalf("Failed due to %v", err)
		}
	} else {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigFile)
		if err != nil {
			log.Fatalf("Failed due to %v", err)
		}
	}

	clientset, err = kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatalf("Failed due to %v", err)
	}

	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		log.Fatalln("Please set env NODE_NAME")
	}

}

func disableCGPUIsolationOrNot(ctx context.Context) (bool, error) {
	disable := false
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return disable, err
	}
	labels := node.ObjectMeta.Labels
	value, ok := labels[EnvNodeLabelForDisableCGPU]
	if ok && value == "true" {
		log.Infof("enable gpusharing mode and disable cgpu mode")
		disable = true
	}
	return disable, nil
}

func preparePatchBytesforNodeStatus(nodeName types.NodeName, oldNode *v1.Node, newNode *v1.Node) ([]byte, error) {
	oldData, err := json.Marshal(oldNode)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal oldData for node %q: %v", nodeName, err)
	}

	// NodeStatus.Addresses is incorrectly annotated as patchStrategy=merge, which
	// will cause strategicpatch.CreateTwoWayMergePatch to create an incorrect patch
	// if it changed.
	manuallyPatchAddresses := (len(oldNode.Status.Addresses) > 0) && !equality.Semantic.DeepEqual(oldNode.Status.Addresses, newNode.Status.Addresses)

	// Reset spec to make sure only patch for Status or ObjectMeta is generated.
	// Note that we don't reset ObjectMeta here, because:
	// 1. This aligns with Nodes().UpdateStatus().
	// 2. Some component does use this to update node annotations.
	diffNode := newNode.DeepCopy()
	diffNode.Spec = oldNode.Spec
	if manuallyPatchAddresses {
		diffNode.Status.Addresses = oldNode.Status.Addresses
	}
	newData, err := json.Marshal(diffNode)
	if err != nil {
		return nil, fmt.Errorf("failed to Marshal newData for node %q: %v", nodeName, err)
	}

	patchBytes, err := strategicpatch.CreateTwoWayMergePatch(oldData, newData, v1.Node{})
	if err != nil {
		return nil, fmt.Errorf("failed to CreateTwoWayMergePatch for node %q: %v", nodeName, err)
	}
	if manuallyPatchAddresses {
		patchBytes, err = fixupPatchForNodeStatusAddresses(patchBytes, newNode.Status.Addresses)
		if err != nil {
			return nil, fmt.Errorf("failed to fix up NodeAddresses in patch for node %q: %v", nodeName, err)
		}
	}

	return patchBytes, nil
}

func fixupPatchForNodeStatusAddresses(patchBytes []byte, addresses []v1.NodeAddress) ([]byte, error) {
	var patchMap map[string]interface{}
	if err := json.Unmarshal(patchBytes, &patchMap); err != nil {
		return nil, err
	}

	addrBytes, err := json.Marshal(addresses)
	if err != nil {
		return nil, err
	}
	var addrArray []interface{}
	if err := json.Unmarshal(addrBytes, &addrArray); err != nil {
		return nil, err
	}
	addrArray = append(addrArray, map[string]interface{}{"$patch": "replace"})

	status := patchMap["status"]
	if status == nil {
		status = map[string]interface{}{}
		patchMap["status"] = status
	}
	statusMap, ok := status.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected data in patch")
	}
	statusMap["addresses"] = addrArray

	return json.Marshal(patchMap)
}

// PatchNodeStatus patches node status.
func PatchNodeStatus(ctx context.Context, c corev1.CoreV1Interface, nodeName types.NodeName, oldNode *v1.Node, newNode *v1.Node) (*v1.Node, []byte, error) {
	patchBytes, err := preparePatchBytesforNodeStatus(nodeName, oldNode, newNode)
	if err != nil {
		return nil, nil, err
	}

	updatedNode, err := c.Nodes().Patch(ctx, string(nodeName), types.StrategicMergePatchType, patchBytes, metav1.PatchOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to patch status %q for node %q: %v", patchBytes, nodeName, err)
	}
	return updatedNode, patchBytes, nil
}

func patchGPUCount(ctx context.Context, gpuCount int) error {
	node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return err
	}

	if val, ok := node.Status.Capacity[resourceCount]; ok {
		if val.Value() == int64(gpuCount) {
			log.Infof("No need to update Capacity %s", resourceCount)
			return nil
		}
	}

	newNode := node.DeepCopy()
	newNode.Status.Capacity[resourceCount] = *resource.NewQuantity(int64(gpuCount), resource.DecimalSI)
	newNode.Status.Allocatable[resourceCount] = *resource.NewQuantity(int64(gpuCount), resource.DecimalSI)
	// content := fmt.Sprintf(`[{"op": "add", "path": "/status/capacity/aliyun.com~gpu-count", "value": "%d"}]`, gpuCount)
	// _, err = clientset.CoreV1().Nodes().PatchStatus(nodeName, []byte(content))
	_, _, err = PatchNodeStatus(ctx, clientset.CoreV1(), types.NodeName(nodeName), node, newNode)
	if err != nil {
		log.Infof("Failed to update Capacity %s.", resourceCount)
	} else {
		log.Infof("Updated Capacity %s successfully.", resourceCount)
	}
	return err
}

func getPodList(kubeletClient *client.KubeletClient) (*v1.PodList, error) {
	podList, err := kubeletClient.GetNodeRunningPods()
	if err != nil {
		return nil, err
	}

	list, _ := json.Marshal(podList)
	log.V(8).Infof("get pods list %v", string(list))
	resultPodList := &v1.PodList{}
	for _, metaPod := range podList.Items {
		if metaPod.Status.Phase != v1.PodPending {
			continue
		}
		resultPodList.Items = append(resultPodList.Items, metaPod)
	}

	if len(resultPodList.Items) == 0 {
		return nil, fmt.Errorf("not found pending pod")
	}

	return resultPodList, nil
}

func getPodListsByQueryKubelet(ctx context.Context, kubeletClient *client.KubeletClient) (*v1.PodList, error) {
	podList, err := getPodList(kubeletClient)
	for i := 0; i < retries && err != nil; i++ {
		podList, err = getPodList(kubeletClient)
		log.Warningf("failed to get pending pod list, retry")
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		log.Warningf("not found from kubelet /pods api, start to list apiserver")
		podList, err = getPodListsByListAPIServer(ctx)
		if err != nil {
			return nil, err
		}
	}
	return podList, nil
}

func getPodListsByListAPIServer(ctx context.Context) (*v1.PodList, error) {
	selector := fields.SelectorFromSet(fields.Set{"spec.nodeName": nodeName, "status.phase": "Pending"})
	podList, err := clientset.CoreV1().Pods(v1.NamespaceAll).List(ctx, metav1.ListOptions{
		FieldSelector: selector.String(),
		LabelSelector: labels.Everything().String(),
	})
	for i := 0; i < 3 && err != nil; i++ {
		podList, err = clientset.CoreV1().Pods(v1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: selector.String(),
			LabelSelector: labels.Everything().String(),
		})
		time.Sleep(1 * time.Second)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get Pods assigned to node %v", nodeName)
	}

	return podList, nil
}

func getPendingPodsInNode(ctx context.Context, queryKubelet bool, kubeletClient *client.KubeletClient) ([]v1.Pod, error) {
	// pods, err := m.lister.List(labels.Everything())
	// if err != nil {
	// 	return nil, err
	// }
	pods := []v1.Pod{}

	podIDMap := map[types.UID]bool{}

	var podList *v1.PodList
	var err error
	if queryKubelet {
		podList, err = getPodListsByQueryKubelet(ctx, kubeletClient)
		if err != nil {
			return nil, err
		}
	} else {
		podList, err = getPodListsByListAPIServer(ctx)
		if err != nil {
			return nil, err
		}
	}

	log.V(5).Infof("all pod list %v", podList.Items)

	// if log.V(5) {
	for _, pod := range podList.Items {
		if pod.Spec.NodeName != nodeName {
			log.Warningf("Pod name %s in ns %s is not assigned to node %s as expected, it's placed on node %s ",
				pod.Name,
				pod.Namespace,
				nodeName,
				pod.Spec.NodeName)
		} else {
			log.Infof("list pod %s in ns %s in node %s and status is %s",
				pod.Name,
				pod.Namespace,
				nodeName,
				pod.Status.Phase,
			)
			if _, ok := podIDMap[pod.UID]; !ok {
				pods = append(pods, pod)
				podIDMap[pod.UID] = true
			}
		}

	}
	// }

	return pods, nil
}

// pick up the gpushare pod with assigned status is false, and
func getCandidatePods(ctx context.Context, queryKubelet bool, client *client.KubeletClient) ([]*v1.Pod, error) {
	candidatePods := []*v1.Pod{}
	allPods, err := getPendingPodsInNode(ctx, queryKubelet, client)
	if err != nil {
		return candidatePods, err
	}
	for _, pod := range allPods {
		current := pod
		if isGPUMemoryAssumedPod(&current) {
			candidatePods = append(candidatePods, &current)
		}
	}

	if log.V(4) {
		for _, pod := range candidatePods {
			log.Infof("candidate pod %s in ns %s with timestamp %d is found.",
				pod.Name,
				pod.Namespace,
				getAssumeTimeFromPodAnnotation(pod))
		}
	}

	return makePodOrderdByAge(candidatePods), nil
}

// make the pod ordered by GPU assumed time
func makePodOrderdByAge(pods []*v1.Pod) []*v1.Pod {
	newPodList := make(orderedPodByAssumeTime, 0, len(pods))
	for _, v := range pods {
		newPodList = append(newPodList, v)
	}
	sort.Sort(newPodList)
	return []*v1.Pod(newPodList)
}

type orderedPodByAssumeTime []*v1.Pod

func (this orderedPodByAssumeTime) Len() int {
	return len(this)
}

func (this orderedPodByAssumeTime) Less(i, j int) bool {
	return getAssumeTimeFromPodAnnotation(this[i]) <= getAssumeTimeFromPodAnnotation(this[j])
}

func (this orderedPodByAssumeTime) Swap(i, j int) {
	this[i], this[j] = this[j], this[i]
}
