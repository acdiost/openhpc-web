package cluster

import "context"

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
