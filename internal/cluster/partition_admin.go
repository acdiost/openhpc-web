package cluster

import "context"

type PartitionAdmin interface {
	ApplyPartition(context.Context, string, []string) error
	DeletePartition(context.Context, string) error
}
