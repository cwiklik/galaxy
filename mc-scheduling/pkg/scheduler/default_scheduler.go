package scheduler

import (
	"fmt"
	"math/rand"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	cmv1alpha1 "kubestellar/galaxy/clustermetrics/api/v1alpha1"
)

type DefaultScheduler struct{}

type score struct {
	name  string
	score int64
}

type PodResources struct {
	CPURequest    resource.Quantity
	MemoryRequest resource.Quantity
	CPULimit      resource.Quantity
	MemoryLimit   resource.Quantity
}

func NewDefaultScheduler() MultiClusterScheduler {
	return &DefaultScheduler{}
}

const (
	// threshold score for selection
	// if scoreThreshod == 0, always use the top score
	// if 0 < scoreThreshod < 1 select scores within topscore and topscore*scoreThreshod
	scoreThreshod = 0
)

func (d *DefaultScheduler) SelectCluster(podSpecList []*corev1.PodSpec, clusterMetricsList *cmv1alpha1.ClusterMetricsList) string {
	// implement a simple scoring algorithm
	// for each cluster:
	//   reset clusterScore
	//   for each pod:
	//     select node with highest score
	//     if no node fits, skip cluster
	//     update node allocated resource
	//     clusterScore = clusterScore + nodeScore
	// sort and select cluster

	clusterScores := make([]score, 0)
	for _, clusterMetrics := range clusterMetricsList.Items {
		fmt.Printf("name=%s cpu=%s memory=%s\n", clusterMetrics.Name, clusterMetrics.Status.Nodes[0].AllocatedResourceRequests.Cpu(), clusterMetrics.Status.Nodes[0].AllocatedResourceRequests.Memory())
		clusterScore := score{name: clusterMetrics.Name}
		//clusterMetricsCopy := clusterMetrics.DeepCopy()
		for _, podSpec := range podSpecList {
			fmt.Printf("Cpu Request: %s Memory Request %s\n", podSpec.Containers[0].Resources.Requests.Cpu(), podSpec.Containers[0].Resources.Requests.Memory())
			nodeScore := selectNode(podSpec, &clusterMetrics)
			if nodeScore == nil {
				clusterScore.score = 0
				break
			}
			// adjusting for pod scheduled should not be necessary as most tasks are sequential
			// and even when parallel they could still run serialized if not enough capacity
			// so we just need to check that every pod in the workflow can run and get the score
			// updateNodeResourceForPod(podSpec, clusterMetricsCopy, *nodeScore)
			clusterScore.score = clusterScore.score + nodeScore.score
		}
		clusterScores = append(clusterScores, clusterScore)
	}

	cluster := selectCluster(clusterScores)
	fmt.Printf("selected: %s\n", cluster)
	return cluster
}

func selectNode(podSpec *corev1.PodSpec, clusterMetrics *cmv1alpha1.ClusterMetrics) *score {
	scores := scoreNodesForPodSpec(podSpec, clusterMetrics)
	if len(scores) == 0 {
		// no suitable node found
		return nil
	}
	// Sort nodes by their scores
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].score > scores[j].score
	})

	// select node with highest score
	return &scores[0]
}

func selectCluster(clusterScores []score) string {
	fmt.Println(clusterScores)
	if len(clusterScores) == 0 {
		// No suitable cluster found
		return ""
	}

	// Sort clusters by their scores in descending order
	sort.Slice(clusterScores, func(i, j int) bool {
		return clusterScores[i].score > clusterScores[j].score
	})

	// to better distribute load, we randomize selection within a  score threshold
	highestScore := clusterScores[0].score
	scoreThreshold := highestScore - int64(float64(highestScore)*scoreThreshod)

	// Find all indexes with a score within scoreThreshod of the highest score
	var topScoreIndexes []int
	for i, v := range clusterScores {
		if v.score >= scoreThreshold {
			topScoreIndexes = append(topScoreIndexes, i)
		}
	}

	// Shuffle the slice of top score indexes
	rand.Shuffle(len(topScoreIndexes), func(i, j int) {
		topScoreIndexes[i], topScoreIndexes[j] = topScoreIndexes[j], topScoreIndexes[i]
	})

	// Select a random cluster among the top scoring ones
	randomIndex := topScoreIndexes[rand.Intn(len(topScoreIndexes))]
	return clusterScores[randomIndex].name
}

// scoreNodesForPodSpec: assign a score to each node, where a score of 0 means node cannnot run the pod
func scoreNodesForPodSpec(podSpec *corev1.PodSpec, clusterMetrics *cmv1alpha1.ClusterMetrics) []score {
	nodeScores := make([]score, 0)
	for _, node := range clusterMetrics.Status.Nodes {
		nodeScore := computeNodeScoreForPod(node, podSpec)
		nodeScores = append(nodeScores, nodeScore)
	}
	return nodeScores
}

// get total pod resources
func getTotalPodResources(podSpec *corev1.PodSpec) PodResources {
	podRes := PodResources{}
	for _, container := range podSpec.Containers {
		if container.Resources.Requests.Cpu() != nil {
			podRes.CPURequest.Add(*container.Resources.Requests.Cpu())
		}
		if container.Resources.Requests.Memory() != nil {
			podRes.MemoryRequest.Add(*container.Resources.Requests.Memory())
		}

		if container.Resources.Limits.Cpu() != nil {
			podRes.CPULimit.Add(*container.Resources.Limits.Cpu())
		}
		if container.Resources.Limits.Memory() != nil {
			podRes.MemoryLimit.Add(*container.Resources.Limits.Memory())
		}
	}

	adjustResourcesTotalsForInitContainers(podSpec, &podRes)

	return podRes
}

// get the max for resources assocuated with all init containers in a pod
// this should be  compared with the sum of resources for all containers in the pod
func getMaxInitContainersResources(podSpec *corev1.PodSpec) PodResources {
	max := PodResources{}
	for _, container := range podSpec.InitContainers {
		if container.Resources.Requests.Cpu() != nil {
			if max.CPURequest.Cmp(*container.Resources.Requests.Cpu()) == -1 {
				max.CPURequest = *container.Resources.Requests.Cpu()
			}
		}
		if container.Resources.Requests.Memory() != nil {
			if max.MemoryRequest.Cmp(*container.Resources.Requests.Memory()) == -1 {
				max.MemoryRequest = *container.Resources.Requests.Memory()
			}
		}

		if container.Resources.Limits.Cpu() != nil {
			if max.CPULimit.Cmp(*container.Resources.Limits.Cpu()) == -1 {
				max.CPULimit = *container.Resources.Limits.Cpu()
			}
		}
		if container.Resources.Limits.Memory() != nil {
			if max.MemoryLimit.Cmp(*container.Resources.Limits.Memory()) == -1 {
				max.MemoryLimit = *container.Resources.Limits.Memory()
			}
		}
	}
	return max
}

func adjustResourcesTotalsForInitContainers(podSpec *corev1.PodSpec, podRes *PodResources) {
	maxInitContainerResources := getMaxInitContainersResources(podSpec)

	if maxInitContainerResources.CPURequest.Cmp(podRes.CPURequest) == 1 {
		podRes.CPURequest = maxInitContainerResources.CPURequest
	}
	if maxInitContainerResources.MemoryRequest.Cmp(podRes.MemoryRequest) == 1 {
		podRes.MemoryRequest = maxInitContainerResources.MemoryRequest
	}
	if maxInitContainerResources.CPULimit.Cmp(podRes.CPULimit) == 1 {
		podRes.CPULimit = maxInitContainerResources.CPULimit
	}
	if maxInitContainerResources.MemoryLimit.Cmp(podRes.MemoryLimit) == 1 {
		podRes.MemoryLimit = maxInitContainerResources.MemoryLimit
	}
}

// computeNodeScoreForPod: compute the score to run a pod on a node
func computeNodeScoreForPod(node cmv1alpha1.NodeInfo, podSpec *corev1.PodSpec) score {
	nodeScore := score{name: node.Name}

	totalPodResourceRequests := getTotalPodResources(podSpec)
	availableResources := getAvailableNodeResources(node)

	// check if pod fits node. No fit returns a score == 0
	if availableResources.Cpu().Cmp(totalPodResourceRequests.CPURequest) >= 0 &&
		availableResources.Memory().Cmp(totalPodResourceRequests.MemoryRequest) >= 0 {

		cpuScore := availableResources.Cpu().MilliValue() - totalPodResourceRequests.CPURequest.MilliValue()
		memScore := availableResources.Memory().Value() - totalPodResourceRequests.MemoryRequest.Value()

		// Memory.Value() returns the value in bytes, while Cpu().MilliValue() returns 1/1000 of CPU core.
		// To normalize, we assume we can compare milliCPUs with MBs (e.g. 500 mCPU ~ 500 MB), therefore
		// we express memory in MB by dividing by 1,000,000

		nodeScore.score = cpuScore + memScore/1000000
	}

	return nodeScore
}

// getAvailableNodeResources compute available node resources to run new pods
// as (Allocatable resources - Total-requested-resources)
func getAvailableNodeResources(node cmv1alpha1.NodeInfo) corev1.ResourceList {
	availableResources := corev1.ResourceList{}

	availableResources[corev1.ResourceCPU] = *node.AllocatableResources.Cpu()
	availableResources[corev1.ResourceMemory] = *node.AllocatableResources.Memory()

	subtractQuantity(availableResources, corev1.ResourceCPU, *node.AllocatedResourceRequests.Cpu())
	subtractQuantity(availableResources, corev1.ResourceMemory, *node.AllocatedResourceRequests.Memory())

	return availableResources
}

// updateNodeResourceForPod - update the AllocatedResourceRequests to account for a pod that has been scheduled on the node
func updateNodeResourceForPod(podSpec *corev1.PodSpec, clusterMetrics *cmv1alpha1.ClusterMetrics, nodeScore score) {
	for _, nodeInfo := range clusterMetrics.Status.Nodes {
		if nodeInfo.Name == nodeScore.name {
			podResources := getTotalPodResources(podSpec)
			subtractQuantity(nodeInfo.AllocatedResourceRequests, corev1.ResourceCPU, podResources.CPURequest)
			subtractQuantity(nodeInfo.AllocatedResourceRequests, corev1.ResourceMemory, podResources.MemoryLimit)
			break
		}
	}
}

// subtract from a  quantity in a resourceList
func subtractQuantity(resourceList corev1.ResourceList, resourceName corev1.ResourceName, resourceQuantity resource.Quantity) {
	if quantityToUpdate, ok := resourceList[resourceName]; ok {
		quantityToUpdate.Sub(resourceQuantity)
		resourceList[resourceName] = quantityToUpdate
	}
}
