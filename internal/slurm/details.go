package slurm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/acdiost/openhpc-web/internal/cluster"
)

const maxDetailFieldLength = 1024

type slurmNumber struct {
	Set      bool  `json:"set"`
	Infinite bool  `json:"infinite"`
	Number   int64 `json:"number"`
}

func (n *slurmNumber) UnmarshalJSON(data []byte) error {
	type objectNumber slurmNumber
	var object objectNumber
	if len(data) > 0 && data[0] == '{' {
		if err := json.Unmarshal(data, &object); err != nil {
			return err
		}
		*n = slurmNumber(object)
		return nil
	}
	var number int64
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	*n = slurmNumber{Set: true, Number: number}
	return nil
}

type sinfoJSON struct {
	Errors []json.RawMessage `json:"errors"`
	Sinfo  []struct {
		Nodes struct {
			Names []string `json:"nodes"`
		} `json:"nodes"`
		Partition struct {
			Name string `json:"name"`
		} `json:"partition"`
		Node struct {
			State []string `json:"state"`
		} `json:"node"`
		CPUs struct {
			Allocated int `json:"allocated"`
			Total     int `json:"total"`
		} `json:"cpus"`
		Memory struct {
			Maximum int `json:"maximum"`
		} `json:"memory"`
		GRES struct {
			Total string `json:"total"`
		} `json:"gres"`
	} `json:"sinfo"`
}

type squeueJSON struct {
	Errors []json.RawMessage `json:"errors"`
	Jobs   []struct {
		ID          int64       `json:"job_id"`
		Name        string      `json:"name"`
		User        string      `json:"user_name"`
		UserID      slurmNumber `json:"user_id"`
		GroupID     slurmNumber `json:"group_id"`
		Account     string      `json:"account"`
		Partition   string      `json:"partition"`
		State       []string    `json:"job_state"`
		CPUs        slurmNumber `json:"cpus"`
		SubmitTime  slurmNumber `json:"submit_time"`
		Eligible    slurmNumber `json:"eligible_time"`
		StartTime   slurmNumber `json:"start_time"`
		EndTime     slurmNumber `json:"end_time"`
		TimeLimit   slurmNumber `json:"time_limit"`
		NodeCount   slurmNumber `json:"node_count"`
		Nodes       string      `json:"nodes"`
		StateReason string      `json:"state_reason"`
		WorkDir     string      `json:"current_working_directory"`
		StdOut      string      `json:"stdout_expanded"`
		StdErr      string      `json:"stderr_expanded"`
		Command     string      `json:"command"`
	} `json:"jobs"`
}

type nodeSnapshot struct {
	Nodes          []cluster.Node
	PartitionNodes []cluster.Node
}

func (c *Client) Nodes(parent context.Context) ([]cluster.Node, error) {
	snapshot, err := c.loadNodeSnapshot(parent)
	return append([]cluster.Node(nil), snapshot.Nodes...), err
}

func (c *Client) loadNodeSnapshot(parent context.Context) (nodeSnapshot, error) {
	return c.nodesCache.get(parent, func(ctx context.Context) (nodeSnapshot, error) {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		output, err := c.run(ctx, "sinfo", "--Node", "--json")
		if err != nil {
			return nodeSnapshot{}, fmt.Errorf("read Slurm nodes: %w", err)
		}
		snapshot, err := parseNodesJSON(output)
		if err != nil {
			return nodeSnapshot{}, fmt.Errorf("parse Slurm nodes: %w", err)
		}
		return snapshot, nil
	})
}

func (c *Client) Jobs(parent context.Context) ([]cluster.Job, error) {
	jobs, err := c.jobsCache.get(parent, func(ctx context.Context) ([]cluster.Job, error) {
		ctx, cancel := context.WithTimeout(ctx, c.timeout)
		defer cancel()
		output, err := c.run(ctx, "squeue", "--json")
		if err != nil {
			return nil, fmt.Errorf("read Slurm jobs: %w", err)
		}
		jobs, err := parseJobsJSON(output, c.now())
		if err != nil {
			return nil, fmt.Errorf("parse Slurm jobs: %w", err)
		}
		return jobs, nil
	})
	return append([]cluster.Job(nil), jobs...), err
}

func (c *Client) Job(parent context.Context, id int64) (cluster.Job, bool, error) {
	jobs, err := c.Jobs(parent)
	if err != nil {
		return cluster.Job{}, false, err
	}
	wantedID := strconv.FormatInt(id, 10)
	for _, job := range jobs {
		if job.ID == wantedID {
			return job, true, nil
		}
	}
	return cluster.Job{}, false, nil
}

func parseNodesJSON(output []byte) (nodeSnapshot, error) {
	var response sinfoJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return nodeSnapshot{}, fmt.Errorf("decode sinfo JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return nodeSnapshot{}, fmt.Errorf("sinfo JSON reported %d errors", len(response.Errors))
	}
	nodesByName := make(map[string]cluster.Node)
	partitionsByNode := make(map[string]map[string]struct{})
	partitionNodes := make([]cluster.Node, 0, len(response.Sinfo))
	for _, record := range response.Sinfo {
		if len(record.Nodes.Names) == 0 || record.Partition.Name == "" || len(record.Node.State) == 0 {
			return nodeSnapshot{}, errors.New("node name, partition and state are required")
		}
		if record.CPUs.Total <= 0 || record.CPUs.Allocated < 0 || record.CPUs.Allocated > record.CPUs.Total {
			return nodeSnapshot{}, errors.New("node CPU allocation must not exceed a positive total")
		}
		if record.Memory.Maximum < 0 {
			return nodeSnapshot{}, errors.New("node memory must be non-negative")
		}
		if err := validateDetailStrings(append(append([]string{record.Partition.Name, record.GRES.Total}, record.Nodes.Names...), record.Node.State...)); err != nil {
			return nodeSnapshot{}, err
		}
		state := strings.ToLower(record.Node.State[0])
		for _, name := range record.Nodes.Names {
			node := cluster.Node{
				Name: name, Partition: record.Partition.Name, State: state,
				AllocatedCPUs: record.CPUs.Allocated, TotalCPUs: record.CPUs.Total,
				MemoryMB: record.Memory.Maximum, GRES: record.GRES.Total,
				Online: jsonNodeOnline(record.Node.State),
			}
			if existing, found := nodesByName[name]; found {
				if nodeDetails(existing) != nodeDetails(node) {
					return nodeSnapshot{}, fmt.Errorf("node %s has conflicting records", name)
				}
			} else {
				nodesByName[name] = node
			}
			partitionNodes = append(partitionNodes, node)
			partitions := partitionsByNode[name]
			if partitions == nil {
				partitions = make(map[string]struct{})
				partitionsByNode[name] = partitions
			}
			partitions[record.Partition.Name] = struct{}{}
		}
	}
	names := make([]string, 0, len(nodesByName))
	for name := range nodesByName {
		names = append(names, name)
	}
	sort.Strings(names)
	nodes := make([]cluster.Node, 0, len(names))
	for _, name := range names {
		node := nodesByName[name]
		node.Partition = strings.Join(sortedNodePartitions(partitionsByNode[name]), ", ")
		nodes = append(nodes, node)
	}
	return nodeSnapshot{Nodes: nodes, PartitionNodes: partitionNodes}, nil
}

func nodeDetails(node cluster.Node) cluster.Node {
	node.Partition = ""
	return node
}

func sortedNodePartitions(partitions map[string]struct{}) []string {
	names := make([]string, 0, len(partitions))
	for name := range partitions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func parseJobsJSON(output []byte, now time.Time) ([]cluster.Job, error) {
	var response squeueJSON
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("decode squeue JSON: %w", err)
	}
	if len(response.Errors) > 0 {
		return nil, fmt.Errorf("squeue JSON reported %d errors", len(response.Errors))
	}
	jobs := make([]cluster.Job, 0, len(response.Jobs))
	for _, record := range response.Jobs {
		if record.ID <= 0 || record.User == "" || len(record.State) == 0 {
			return nil, errors.New("job ID, user and state are required")
		}
		if record.NodeCount.Number < 0 || record.NodeCount.Number > int64(^uint(0)>>1) {
			return nil, fmt.Errorf("job %d node count is invalid", record.ID)
		}
		if (record.UserID.Set && record.UserID.Number <= 0) || (record.GroupID.Set && record.GroupID.Number <= 0) || record.CPUs.Number < 0 || record.CPUs.Number > int64(^uint(0)>>1) {
			return nil, fmt.Errorf("job %d user, group, or CPU count is invalid", record.ID)
		}
		userID := int64(0)
		if record.UserID.Set {
			userID = record.UserID.Number
		}
		groupID := int64(0)
		if record.GroupID.Set {
			groupID = record.GroupID.Number
		}
		values := []string{record.Name, record.User, record.Account, record.Partition, record.Nodes, record.StateReason, record.WorkDir, record.StdOut, record.StdErr, record.Command}
		values = append(values, record.State...)
		if err := validateDetailStrings(values); err != nil {
			return nil, err
		}
		nodesOrReason := strings.TrimSpace(record.Nodes)
		if nodesOrReason == "" {
			nodesOrReason = strings.TrimSpace(record.StateReason)
			if nodesOrReason == "" || strings.EqualFold(nodesOrReason, "None") {
				nodesOrReason = "—"
			}
		}
		nodes := strings.TrimSpace(record.Nodes)
		if nodes == "" {
			nodes = "—"
		}
		jobs = append(jobs, cluster.Job{
			ID: strconv.FormatInt(record.ID, 10), Name: record.Name, User: record.User, UserID: userID, GroupID: groupID,
			Account: record.Account, Partition: record.Partition, State: strings.ToUpper(record.State[0]), CPUCount: int(record.CPUs.Number),
			Elapsed: formatElapsed(record.StartTime, now), TimeLimit: formatTimeLimit(record.TimeLimit), NodeCount: int(record.NodeCount.Number),
			Nodes: nodes, NodesOrReason: nodesOrReason, SubmitTime: formatSlurmTimestamp(record.SubmitTime, now.Location(), "—"),
			EligibleTime: formatSlurmTimestamp(record.Eligible, now.Location(), "—"), StartTime: formatSlurmTimestamp(record.StartTime, now.Location(), "—"),
			EndTime: formatSlurmTimestamp(record.EndTime, now.Location(), "Unknown"), WorkDir: displayValue(record.WorkDir),
			StdOut: displayValue(record.StdOut), StdErr: displayValue(record.StdErr), Command: displayValue(record.Command),
		})
	}
	return jobs, nil
}

func formatSlurmTimestamp(value slurmNumber, location *time.Location, unavailable string) string {
	if !value.Set || value.Infinite || value.Number <= 0 {
		return unavailable
	}
	return time.Unix(value.Number, 0).In(location).Format("2006-01-02T15:04:05")
}

func displayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "—"
	}
	return value
}

func jsonNodeOnline(states []string) bool {
	for _, value := range states {
		state := strings.ToLower(value)
		for _, prefix := range []string{"down", "fail", "unknown", "future", "powered_down", "power_down", "powering_down", "no_respond"} {
			if strings.HasPrefix(state, prefix) {
				return false
			}
		}
	}
	return true
}

func formatElapsed(value slurmNumber, now time.Time) string {
	if !value.Set || value.Infinite || value.Number <= 0 || value.Number > now.Unix() {
		return "—"
	}
	return formatSlurmDuration(now.Unix() - value.Number)
}

func formatTimeLimit(value slurmNumber) string {
	if value.Infinite {
		return "UNLIMITED"
	}
	if !value.Set || value.Number < 0 || value.Number > (1<<63-1)/60 {
		return "—"
	}
	return formatSlurmDuration(value.Number * 60)
}

func formatSlurmDuration(seconds int64) string {
	days := seconds / 86400
	hours := seconds % 86400 / 3600
	minutes := seconds % 3600 / 60
	remainingSeconds := seconds % 60
	if days > 0 {
		return fmt.Sprintf("%d-%02d:%02d:%02d", days, hours, minutes, remainingSeconds)
	}
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, remainingSeconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, remainingSeconds)
}

func validateDetailStrings(values []string) error {
	for _, value := range values {
		if len(value) > maxDetailFieldLength {
			return errors.New("Slurm detail field exceeds maximum length")
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return errors.New("Slurm detail field contains control characters")
			}
		}
	}
	return nil
}
