package slurm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

func (c *Client) ApplyPartition(ctx context.Context, name string, nodes []string) error {
	if err := validatePartitionDefinition(name, nodes); err != nil {
		return err
	}
	nodeList := strings.Join(nodes, ",")
	partitionArg := "PartitionName=" + name
	nodeArg := "Nodes=" + nodeList

	showCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if _, err := c.run(showCtx, "scontrol", "show", "partition", name); err == nil {
		_, err = c.run(showCtx, "scontrol", "update", partitionArg, nodeArg)
		if err != nil {
			return fmt.Errorf("update Slurm partition %s: %w", name, err)
		}
		return nil
	}

	createCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if _, err := c.run(createCtx, "scontrol", "create", partitionArg, nodeArg); err != nil {
		return fmt.Errorf("create Slurm partition %s: %w", name, err)
	}
	return nil
}

func validatePartitionDefinition(name string, nodes []string) error {
	if strings.TrimSpace(name) == "" || len(nodes) == 0 {
		return errors.New("partition name and nodes are required")
	}
	values := append([]string{name}, nodes...)
	if err := validateDetailStrings(values); err != nil {
		return err
	}
	for _, value := range values {
		if strings.ContainsAny(value, ", \t\r\n") {
			return errors.New("partition name and nodes must not contain commas or whitespace")
		}
	}
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if _, ok := seen[node]; ok {
			return errors.New("partition nodes must be unique")
		}
		seen[node] = struct{}{}
	}
	return nil
}

var _ cluster.PartitionAdmin = (*Client)(nil)

