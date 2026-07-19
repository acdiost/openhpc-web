package slurm

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func (c *Client) Partitions(ctx context.Context) ([]cluster.Partition, error) {
	snapshot, err := c.loadNodeSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("read partition node snapshot: %w", err)
	}
	return aggregatePartitions(snapshot.PartitionNodes)
}

func aggregatePartitions(nodes []cluster.Node) ([]cluster.Partition, error) {
	byPartition := make(map[string]map[string]cluster.Node)
	for _, node := range nodes {
		if node.Name == "" || node.Partition == "" {
			return nil, errors.New("partition and node name are required")
		}
		if node.TotalCPUs < 0 || node.AllocatedCPUs < 0 || node.AllocatedCPUs > node.TotalCPUs || node.MemoryMB < 0 {
			return nil, fmt.Errorf("node %s has invalid partition resources", node.Name)
		}
		if err := validateDetailStrings([]string{node.Partition, node.Name}); err != nil {
			return nil, err
		}
		partitionNodes := byPartition[node.Partition]
		if partitionNodes == nil {
			partitionNodes = make(map[string]cluster.Node)
			byPartition[node.Partition] = partitionNodes
		}
		if existing, found := partitionNodes[node.Name]; found {
			if existing != node {
				return nil, fmt.Errorf("partition %s node %s has conflicting records", node.Partition, node.Name)
			}
			continue
		}
		partitionNodes[node.Name] = node
	}

	partitions := make([]cluster.Partition, 0, len(byPartition))
	for name, partitionNodes := range byPartition {
		partition := cluster.Partition{Name: name, NodeCount: len(partitionNodes)}
		for _, node := range partitionNodes {
			if node.Online {
				partition.OnlineNodes++
			}
			if err := addPartitionResources(&partition, node); err != nil {
				return nil, err
			}
		}
		if partition.TotalCPUs > 0 {
			partition.CPUUtilization = int(math.Round(float64(partition.AllocatedCPUs) * 100 / float64(partition.TotalCPUs)))
		}
		partitions = append(partitions, partition)
	}
	sort.Slice(partitions, func(i, j int) bool { return partitions[i].Name < partitions[j].Name })
	return partitions, nil
}

func addPartitionResources(partition *cluster.Partition, node cluster.Node) error {
	allocated := int64(node.AllocatedCPUs)
	total := int64(node.TotalCPUs)
	memory := int64(node.MemoryMB)
	if allocated > math.MaxInt64-partition.AllocatedCPUs || total > math.MaxInt64-partition.TotalCPUs || memory > math.MaxInt64-partition.MemoryMB {
		return fmt.Errorf("partition %s resources exceed supported range", partition.Name)
	}
	partition.AllocatedCPUs += allocated
	partition.TotalCPUs += total
	partition.MemoryMB += memory
	return nil
}
