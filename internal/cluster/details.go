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

type PartitionProvider interface {
	Partitions(context.Context) ([]Partition, error)
}

type JobProvider interface {
	Jobs(context.Context) ([]Job, error)
	Job(context.Context, int64) (Job, bool, error)
}
