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
	Account       string
	State         string
	Elapsed       string
	TimeLimit     string
	NodeCount     int
	NodesOrReason string
}

type NodeProvider interface {
	Nodes(context.Context) ([]Node, error)
}

type JobProvider interface {
	Jobs(context.Context) ([]Job, error)
	Job(context.Context, int64) (Job, bool, error)
}
