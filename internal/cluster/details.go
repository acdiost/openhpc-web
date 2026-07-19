package cluster

import "context"

type Node struct {
	Name          string
	Partition     string
	State         string
	AllocatedCPUs int
	TotalCPUs     int
	MemoryMB      int
	GRES          string
	Online        bool
}

type Job struct {
	ID            string
	Name          string
	User          string
	UserID        int64
	GroupID       int64
	Account       string
	Partition     string
	State         string
	CPUCount      int
	Elapsed       string
	TimeLimit     string
	NodeCount     int
	Nodes         string
	NodesOrReason string
	SubmitTime    string
	EligibleTime  string
	StartTime     string
	EndTime       string
	WorkDir       string
	StdOut        string
	StdErr        string
	Command       string
}

type JobResourceStep struct {
	Step            string `json:"step"`
	AveCPU          string `json:"ave_cpu"`
	AveCPUSeconds   int64  `json:"ave_cpu_seconds"`
	TotalCPU        string `json:"total_cpu"`
	TotalCPUSeconds int64  `json:"total_cpu_seconds"`
	AveRSS          string `json:"ave_rss"`
	AveRSSBytes     int64  `json:"ave_rss_bytes"`
	MaxRSS          string `json:"max_rss"`
	MaxRSSBytes     int64  `json:"max_rss_bytes"`
	AveVMSize       string `json:"ave_vm_size"`
	AveVMSizeBytes  int64  `json:"ave_vm_size_bytes"`
	MaxVMSize       string `json:"max_vm_size"`
	MaxVMSizeBytes  int64  `json:"max_vm_size_bytes"`
}

type JobResourceUsage struct {
	JobID           string            `json:"job_id"`
	SampledAt       string            `json:"sampled_at"`
	TotalCPUSeconds int64             `json:"total_cpu_seconds"`
	MaxRSSBytes     int64             `json:"max_rss_bytes"`
	Steps           []JobResourceStep `json:"steps"`
}

type Partition struct {
	Name           string
	NodeCount      int
	OnlineNodes    int
	AllocatedCPUs  int64
	TotalCPUs      int64
	MemoryMB       int64
	CPUUtilization int
}

type NodeProvider interface {
	Nodes(context.Context) ([]Node, error)
}

type NodeAdmin interface {
	SetNodeState(context.Context, string, NodeState, string) error
}

type NodeState string

const (
	NodeStateDown   NodeState = "down"
	NodeStateDrain  NodeState = "drain"
	NodeStateResume NodeState = "resume"
)

type PartitionProvider interface {
	Partitions(context.Context) ([]Partition, error)
}

type JobProvider interface {
	Jobs(context.Context) ([]Job, error)
	Job(context.Context, int64) (Job, bool, error)
}

type JobResourceProvider interface {
	JobResourceUsage(context.Context, int64) (JobResourceUsage, error)
}

type JobCanceler interface {
	CancelJob(context.Context, int64) error
}
