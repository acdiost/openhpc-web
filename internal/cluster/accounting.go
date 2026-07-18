package cluster

import "context"

import "time"

type Account struct {
	Name             string
	Description      string
	Organization     string
	CoordinatorCount int
	AssociationCount int
}

type SlurmUser struct {
	Name               string
	AdministratorLevel string
	DefaultAccount     string
	DefaultWCKey       string
	AssociationCount   int
}

type AccountDirectory struct {
	Accounts []Account
	Users    []SlurmUser
}

type Association struct {
	ID        int64
	Cluster   string
	Account   string
	User      string
	Partition string
}

type QoS struct {
	Name             string
	Description      string
	Priority         int64
	UsageFactor      float64
	MaxJobs          int64
	MaxJobsUnlimited bool
}

type AccountingProvider interface {
	AccountDirectory(context.Context) (AccountDirectory, error)
	QoS(context.Context) ([]QoS, error)
}

type AssociationProvider interface {
	Associations(context.Context) ([]Association, error)
}

type CoreHourPeriod string

const (
	CoreHourPeriod24Hours CoreHourPeriod = "24h"
	CoreHourPeriod7Days   CoreHourPeriod = "7d"
	CoreHourPeriod30Days  CoreHourPeriod = "30d"
)

type CoreHourGroup struct {
	Name            string
	CoreSeconds     int64
	AllocationCount int
}

type CoreHourSummary struct {
	From            time.Time
	To              time.Time
	CoreSeconds     int64
	AllocationCount int
	Accounts        []CoreHourGroup
	Users           []CoreHourGroup
}

type CoreHourProvider interface {
	CoreHours(context.Context, CoreHourPeriod) (CoreHourSummary, error)
}
