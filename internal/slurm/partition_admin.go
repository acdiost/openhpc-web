package slurm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

const maxNodeReasonLength = 256

func (c *Client) ApplyPartition(ctx context.Context, name string, nodes []string) error {
	if err := validatePartitionDefinition(name, nodes); err != nil {
		return err
	}
	nodeList := strings.Join(nodes, ",")
	partitionArg := "partitionname=" + name
	nodeArg := "nodes=" + nodeList

	createCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if _, err := c.run(createCtx, "scontrol", "create", partitionArg, nodeArg); err == nil {
		return nil
	} else {
		updateCtx, cancelUpdate := context.WithTimeout(ctx, c.timeout)
		defer cancelUpdate()
		if _, updateErr := c.run(updateCtx, "scontrol", "update", partitionArg, nodeArg); updateErr == nil {
			return nil
		} else {
			return fmt.Errorf("apply Slurm partition %s: create: %w; update: %v", name, err, updateErr)
		}
	}
}

func (c *Client) DeletePartition(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("partition name is required")
	}
	if err := validateDetailStrings([]string{name}); err != nil {
		return err
	}
	if strings.ContainsAny(name, ", \t\r\n") {
		return errors.New("partition name must not contain commas or whitespace")
	}
	deleteCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if _, err := c.run(deleteCtx, "scontrol", "delete", "partitionname="+name); err != nil {
		return fmt.Errorf("delete Slurm partition %s: %w", name, err)
	}
	return nil
}

func (c *Client) SetNodeState(ctx context.Context, name string, state cluster.NodeState, reason string) error {
	if err := validateNodeName(name); err != nil {
		return err
	}
	reason = strings.TrimSpace(reason)
	if err := validateNodeState(state, reason); err != nil {
		return err
	}
	updateCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	args := []string{"update", "nodename=" + name, "state=" + string(state)}
	if reason != "" {
		args = append(args, "reason="+reason)
	}
	if _, err := c.run(updateCtx, "scontrol", args...); err != nil {
		return fmt.Errorf("set Slurm node %s state: %w", name, err)
	}
	c.nodesCache.invalidate()
	return nil
}

func validateNodeState(state cluster.NodeState, reason string) error {
	switch state {
	case cluster.NodeStateResume:
		if reason != "" {
			return errors.New("node state reason is not allowed for resume")
		}
		return nil
	case cluster.NodeStateDown, cluster.NodeStateDrain:
		if reason == "" {
			return errors.New("node state reason is required")
		}
		if len(reason) > maxNodeReasonLength {
			return errors.New("node state reason exceeds maximum length")
		}
		return validateDetailStrings([]string{reason})
	default:
		return errors.New("invalid node state")
	}
}

func validateNodeName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("node name is required")
	}
	if err := validateDetailStrings([]string{name}); err != nil {
		return err
	}
	if strings.ContainsAny(name, ", \t\r\n") {
		return errors.New("node name must not contain commas or whitespace")
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
var _ cluster.NodeAdmin = (*Client)(nil)
